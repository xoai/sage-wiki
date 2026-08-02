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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
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
	// manifest.json is written ONCE (N6: re-enable keeps the original
	// CreatedAt rather than rewriting it).
	exists, err = m.client.HeadObject(ctx, m.cfg.Bucket, ManifestKey(prefix))
	if err != nil {
		return fmt.Errorf("mirror enable: check manifest: %w", err)
	}
	if !exists {
		mb, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return fmt.Errorf("mirror enable: marshal manifest: %w", err)
		}
		if err := m.client.PutObject(ctx, m.cfg.Bucket, ManifestKey(prefix), mb); err != nil {
			return fmt.Errorf("mirror enable: PUT manifest.json: %w", err)
		}
	}

	// Bootstrap generation 1 if a database exists (pre-compile workspaces
	// bootstrap on the first ship pass instead).
	if _, err := os.Stat(m.dbPath()); err != nil {
		return nil // manifest written; nothing to snapshot yet
	}
	return m.bootstrapGeneration1(ctx, "mirror enable")
}

// bootstrapGeneration1 is the mutex-taking WRAPPER used by Enable.
// shipPass calls bootstrapGeneration1Locked directly (it already holds the
// mutex — re-acquiring self-deadlocks via flock fd contention, F-113).
func (m *Mirror) bootstrapGeneration1(ctx context.Context, what string) error {
	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer mutex.Release()
	return m.bootstrapGeneration1Locked(ctx, what)
}

// bootstrapGeneration1Locked snapshots the db and commits generation 1.
// The caller MUST hold the ship-mutex (wrapper above or shipPass).
func (m *Mirror) bootstrapGeneration1Locked(ctx context.Context, what string) error {
	prefix := NormalizePrefix(m.cfg.Prefix)

	// Absence re-check UNDER the mutex (pass-2 MAJOR-3): two concurrent
	// enables (or an enable racing a first-pass bootstrap) must not
	// double-commit generation 1 — the second writer's chain would replay
	// onto the first's snapshot (franken-db, verify VALID).
	exists, err := m.client.HeadObject(ctx, m.cfg.Bucket, StateKey(prefix))
	if err != nil {
		return fmt.Errorf("%s: re-check remote state: %w", what, err)
	}
	if exists {
		return ErrAlreadyEnabled
	}

	// Manifest identity check (F-118): never commit a generation under a
	// foreign manifest (repointed config at someone else's bucket).
	mb, err := m.client.GetObject(ctx, m.cfg.Bucket, ManifestKey(prefix))
	if err != nil {
		return fmt.Errorf("%s: read manifest.json: %w", what, err)
	}
	var man MirrorManifest
	if err := json.Unmarshal(mb, &man); err != nil {
		return fmt.Errorf("%s: parse manifest.json: %w", what, err)
	}
	if man.FormatVersion != FormatVersion {
		return fmt.Errorf("%s: manifest format_version %d, want %d (foreign or legacy bucket — refusing to commit)", what, man.FormatVersion, FormatVersion)
	}
	// Full identity check (pass-4 N1): a same-format foreign manifest must
	// not receive this workspace's gen-1 chain either.
	if man.Tool != "sage-wiki" || man.Workspace != filepath.Base(m.dir) {
		return fmt.Errorf("%s: manifest belongs to tool=%q workspace=%q, not this workspace %q — refusing to commit under a foreign manifest",
			what, man.Tool, man.Workspace, filepath.Base(m.dir))
	}

	snapBytes, err := snapshotDatabase(ctx, m.dbPath())
	if err != nil {
		return fmt.Errorf("%s: snapshot: %w", what, err)
	}
	compressed, err := zstdEncode(snapBytes)
	if err != nil {
		return fmt.Errorf("%s: compress snapshot: %w", what, err)
	}
	now := time.Now().UTC()

	snapKey := SnapshotKey(prefix, 1)
	sha, err := m.putObjectShasum(ctx, snapKey, compressed)
	if err != nil {
		return fmt.Errorf("%s: PUT snapshot: %w", what, err)
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
		Objects:           map[string]ObjectRef{},
		Vectors:           map[string]ObjectRef{},
		UpdatedAt:         now,
		RetainGenerations: m.cfg.RetainGenerations,
	}
	sb, err := MarshalState(st)
	if err != nil {
		return fmt.Errorf("%s: marshal state: %w", what, err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(prefix), sb); err != nil {
		return fmt.Errorf("%s: PUT mirror-state: %w", what, err)
	}

	// Local bookkeeping: the db is quiesced post-snapshot, so record its
	// content hash for fold detection.
	dbHash, dbSize, hashErr := hashFile(m.dbPath())
	if hashErr != nil {
		return fmt.Errorf("%s: hash local db: %w", what, hashErr)
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
		return fmt.Errorf("%s: save local state: %w", what, err)
	}
	return nil
}

// DeferredError signals a rotation deferred under a busy writer (spec.md:
// VACUUM-fallback busy policy). The caller reports defers via status.
type DeferredError struct{ Reason string }

func (e *DeferredError) Error() string { return "mirror: rotation deferred: " + e.Reason }

// snapOptions tunes snapshotForRotation (tests force the fallback + shorten
// the busy policy).
type snapOptions struct {
	forceFallback bool
	busyTimeout   time.Duration
	maxRetries    int
	vacuumDestDir string // override for failure injection (default: os.TempDir)
	local         *LocalState
	localPath     string
}

// snapshotDatabase produces a consistent copy of the db at path via the
// online backup API (primary path, Task 1 probe).
func snapshotDatabase(ctx context.Context, path string) ([]byte, error) {
	return snapshotForRotation(ctx, path, snapOptions{
		busyTimeout: 5 * time.Second,
		maxRetries:  3,
	})
}

// snapshotForRotation is the rotation-facing snapshot: backup API first,
// VACUUM INTO fallback with busy_timeout + bounded retries, then a typed
// DeferredError — incrementing consecutive_defers BEFORE abandoning
// (spec.md: a crash in that window can only over-count, never silently
// under-count).
func snapshotForRotation(ctx context.Context, dbPath string, opts snapOptions) ([]byte, error) {
	if opts.maxRetries < 1 {
		opts.maxRetries = 1
	}
	if !opts.forceFallback {
		b, err := snapshotViaBackup(ctx, dbPath)
		if err == nil {
			return b, nil
		} else {
			// Loud once per process (pass-2 finding): the downgrade changes
			// busy-writer semantics (VACUUM defers; backup API doesn't).
			backupDowngradeOnce.Do(func() {
				slog.Warn("mirror: backup API unavailable, using VACUUM INTO fallback", "err", err)
			})
		}
	}
	err := snapshotViaVacuum(ctx, dbPath, opts)
	if err == nil {
		return readTempSnapshot()
	}
	if opts.local != nil && opts.localPath != "" {
		opts.local.ConsecutiveDefers++
		if serr := SaveLocalState(opts.localPath, opts.local); serr != nil {
			return nil, fmt.Errorf("mirror: persist defer counter: %w", serr)
		}
	}
	return nil, &DeferredError{Reason: err.Error()}
}

// lastVacuumPath tracks the temp file snapshotViaVacuum wrote (single-flight
// per process — the ship-mutex serializes callers).
var lastVacuumPath string

func readTempSnapshot() ([]byte, error) {
	b, err := os.ReadFile(lastVacuumPath)
	os.Remove(lastVacuumPath)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	return b, nil
}

// snapshotViaBackup is the Task-1 backup-API path into a temp file.
func snapshotViaBackup(ctx context.Context, dbPath string) ([]byte, error) {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
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

// snapshotViaVacuum runs VACUUM INTO under busy_timeout with bounded retries
// (spec.md fallback semantics: single read transaction, consistent).
func snapshotViaVacuum(ctx context.Context, dbPath string, opts snapOptions) error {
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()
	tmpDir := opts.vacuumDestDir
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	tmp := filepath.Join(tmpDir, fmt.Sprintf("sage-mirror-vacuum-%d.db", time.Now().UnixNano()))
	lastVacuumPath = tmp
	if _, err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout=%d", opts.busyTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	// F-086: SQL string literal quoting — TMPDIR is environment-derived.
	sqlTmp := strings.ReplaceAll(tmp, "'", "''")
	var lastErr error
	for attempt := 0; attempt < opts.maxRetries; attempt++ {
		if _, err := db.ExecContext(ctx, "VACUUM INTO '"+sqlTmp+"'"); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	os.Remove(tmp)
	return fmt.Errorf("VACUUM INTO busy after %d attempt(s): %w", opts.maxRetries, lastErr)
}

// hashFile streams a file's SHA-256 and size.
func hashFile(path string) (sha string, size int64, err error) {
	hashFileCalls.Add(1)
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

// hashFileCalls counts file-hash operations (F-082's idle-cost proof in
// tests; production reads are unaffected).
var hashFileCalls atomic.Int64

// backupDowngradeOnce limits the backup-API→VACUUM downgrade log to once
// per process.
var backupDowngradeOnce sync.Once
