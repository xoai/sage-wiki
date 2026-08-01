package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
	_ "modernc.org/sqlite"
)

// MirrorManifest is <prefix>manifest.json — the enable-time descriptor,
// written once (spec.md §Data model).
type MirrorManifest struct {
	FormatVersion int       `json:"format_version"`
	Tool          string    `json:"tool"`
	Workspace     string    `json:"workspace"`
	CreatedAt     time.Time `json:"created_at"`
	Encrypted     bool      `json:"encrypted"`
}

// ErrAlreadyEnabled reports an enable against a mirror that has state.
var ErrAlreadyEnabled = errors.New("mirror: already enabled (mirror-state.json exists)")

// mirrorOps is the real ops implementation behind the facade; methods grow
// per plan tasks (enable now; ship/snapshot Task 12/13; status/verify Task 9;
// hydrate Task 19).
type mirrorOps struct{ m *Mirror }

// Stubs for ops methods wired by later tasks (ship/snapshot Tasks 12/13,
// hydrate Task 19). Loud, never silent.
func (o *mirrorOps) Ship(ctx context.Context, batch pkmirror.ChangeBatch) error {
	return &NotReadyError{Op: "ship"}
}

func (o *mirrorOps) Snapshot(ctx context.Context) (pkmirror.SnapshotID, error) {
	return "", &NotReadyError{Op: "snapshot"}
}

func (o *mirrorOps) Hydrate(ctx context.Context, dst string) error {
	return &NotReadyError{Op: "hydrate"}
}

func init() {
	// Wire the facade to the real ops in Open (kept here so the wiring lives
	// next to the implementation).
	openWiresOps = func(m *Mirror) { m.ops = &mirrorOps{m: m} }
}

// Enable validates connectivity, writes manifest.json, and bootstraps
// generation 1 (snapshot + initial state commit) when the workspace has a
// database. The initial state is written LAST (write-then-commit).
func (o *mirrorOps) Enable(ctx context.Context) error {
	m := o.m
	prefix := NormalizePrefix(m.cfg.Prefix)

	// Already enabled? Remote state existing means yes.
	exists, err := m.client.HeadObject(ctx, m.cfg.Bucket, StateKey(prefix))
	if err != nil {
		return fmt.Errorf("mirror enable: check remote state: %w", err)
	}
	if exists {
		return ErrAlreadyEnabled
	}

	// manifest.json first — connectivity + write proof.
	man := MirrorManifest{
		FormatVersion: FormatVersion,
		Tool:          "sage-wiki",
		Workspace:     filepath.Base(m.dir),
		CreatedAt:     time.Now().UTC(),
		Encrypted:     m.cfg.Encryption.Enabled,
	}
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("mirror enable: marshal manifest: %w", err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, ManifestKey(prefix), mb); err != nil {
		return fmt.Errorf("mirror enable: PUT manifest.json: %w", err)
	}

	// Bootstrap generation 1 if a database exists (pre-compile workspaces
	// bootstrap on the first ship pass instead).
	dbPath := filepath.Join(m.dir, ".sage", "wiki.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil // manifest written; nothing to snapshot yet
	}

	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return fmt.Errorf("mirror enable: %w", err)
	}
	defer mutex.Release()

	snapBytes, err := snapshotDatabase(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("mirror enable: snapshot: %w", err)
	}
	compressed, err := zstdEncode(snapBytes)
	if err != nil {
		return fmt.Errorf("mirror enable: compress snapshot: %w", err)
	}
	sha := sha256HexBytes(compressed)
	now := time.Now().UTC()

	snapKey := SnapshotKey(prefix, 1)
	if err := m.client.PutObject(ctx, m.cfg.Bucket, snapKey, compressed); err != nil {
		return fmt.Errorf("mirror enable: PUT snapshot: %w", err)
	}

	st := &State{
		FormatVersion: FormatVersion,
		Generation:    1,
		DB: DBState{
			Snapshot:       snapKey,
			SnapshotSHA256: sha,
			CreatedAt:      now,
			WAL:            []WALSegmentRef{},
		},
		Objects:   map[string]ObjectRef{},
		Vectors:   map[string]ObjectRef{},
		UpdatedAt: now,
	}
	sb, err := MarshalState(st)
	if err != nil {
		return fmt.Errorf("mirror enable: marshal state: %w", err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(prefix), sb); err != nil {
		return fmt.Errorf("mirror enable: PUT mirror-state: %w", err)
	}

	// Local bookkeeping: the db is quiesced post-snapshot, so record its
	// content hash for fold detection.
	dbHash, dbSize, hashErr := hashFile(dbPath)
	if hashErr != nil {
		return fmt.Errorf("mirror enable: hash local db: %w", hashErr)
	}
	m.local.Generation = 1
	m.local.WALSalt = 0
	m.local.WALOffset = 0
	m.local.LastDBSHA256 = dbHash
	m.local.LastDBSize = dbSize
	m.local.LastSegmentSeq = 0
	m.local.LastRotationAt = now
	m.local.PendingRotation = false
	m.local.ConsecutiveDefers = 0
	if err := SaveLocalState(localStatePath(m.dir), m.local); err != nil {
		return fmt.Errorf("mirror enable: save local state: %w", err)
	}
	return nil
}

// snapshotDatabase produces a consistent copy of the db at path via the
// online backup API (Task 11 adds the VACUUM INTO fallback).
func snapshotDatabase(ctx context.Context, path string) ([]byte, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("sage-mirror-snap-%d.db", time.Now().UnixNano()))
	defer os.Remove(tmp)
	if err := snapshotViaBackupAPI(db, tmp); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	return b, nil
}

// hashFile streams a file's SHA-256 and size.
func hashFile(path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func zstdEncode(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func zstdDecode(b []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
