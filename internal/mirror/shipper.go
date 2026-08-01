package mirror

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// shipResult reports one ship pass (best-effort; callers warn on error,
// never fail the invoking command).
type shipResult struct {
	SealedSegments    int
	Rotated           bool
	PendingRotation   bool
	HashedDB          bool // the full db hash ran this pass (cost observability)
	ObjectsShipped    int
	ObjectsTombstoned int
	Warnings          []string
}

// Ship implements the pkg seam: a ChangeBatch is accepted for interface
// conformance; shipping is diff-driven (spec.md §Ship trigger), so the batch
// payload is advisory only.
func (o *mirrorOps) Ship(ctx context.Context, batch pkmirror.ChangeBatch) error {
	_, err := o.m.shipPass(ctx)
	return err
}

// Snapshot forces a new generation (4-step rotation under the ship-mutex).
func (o *mirrorOps) Snapshot(ctx context.Context) (pkmirror.SnapshotID, error) {
	m := o.m
	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return "", err
	}
	defer mutex.Release()
	st, err := m.remoteState(ctx)
	if err != nil {
		return "", err
	}
	if err := m.rotate(ctx, st); err != nil {
		return "", err
	}
	return pkmirror.SnapshotID(fmt.Sprintf("generation-%d", st.Generation+1)), nil
}

// shipPass is the single-leader ship cycle (spec.md §Close-fold handling,
// steps in order): reconcile → seal → short-circuit → fold rules →
// debounce/rotation → atomic local commit.
func (m *Mirror) shipPass(ctx context.Context) (shipResult, error) {
	var res shipResult
	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return res, err
	}
	defer mutex.Release()

	st, err := m.remoteState(ctx)
	if err != nil {
		return res, err
	}
	local := m.local

	// --- Step 2: reconcile pending_rotation (spec §Close-fold (5)) ---
	if local.PendingRotation {
		if st.Generation > local.Generation {
			// The rotation committed remotely before our local write (crash
			// window) — adopt per field, no spurious rotation.
			local.Generation = st.Generation
			local.LastSegmentSeq = len(st.DB.WAL)
			local.WALSalt, local.WALOffset = m.adoptWAL()
			hash, size, herr := hashFile(m.dbPath())
			if herr == nil {
				local.LastDBSHA256, local.LastDBSize = hash, size
				res.HashedDB = true
			}
			local.LastRotationAt = st.DB.CreatedAt
			local.PendingRotation = false
			local.ConsecutiveDefers = 0
			if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
				return res, err
			}
		} else if m.now().Sub(local.LastRotationAt) >= m.cfg.MinRotationInterval {
			// Debounce elapsed: rotate now.
			if err := m.rotate(ctx, st); err != nil {
				return res, err
			}
			res.Rotated = true
			res.Warnings = m.lastPruneWarnings
			return res, nil
		} else {
			// Still debouncing — nothing else to do this pass.
			res.PendingRotation = true
			return res, nil
		}
	}

	// --- Object sync (objects → segments → state, per spec ordering) ---
	objectsDirty := false
	if m.src != nil {
		changes, _, err := m.src.Changes(ctx, ChangeToken{
			Committed:        st.Objects,
			CommittedVectors: st.Vectors,
		})
		if err != nil {
			return res, fmt.Errorf("ship: detect changes: %w", err)
		}
		if err := m.shipObjects(ctx, st, changes, &res); err != nil {
			return res, err
		}
		objectsDirty = res.ObjectsShipped+res.ObjectsTombstoned > 0
	}

	// --- WAL adoption: unknown incarnation (post-bootstrap/rotation/fold)
	// → adopt the current WAL's salt with offset 0 (its frames are
	// generation-start content, sealed below from byte 0 incl. header).
	walHdr, walSize, walErr := WALInfoFromFile(m.walPath())
	walPresent := walErr == nil
	if walErr != nil && !errors.Is(walErr, os.ErrNotExist) {
		return res, walErr
	}
	if walPresent && local.WALSalt == 0 {
		local.WALSalt = walHdr.SaltID()
		local.WALOffset = 0
	}

	// --- Branch (1): seal new WAL frames at the recorded (salt, offset) ---
	if walPresent && walHdr.SaltID() == local.WALSalt && walSize > local.WALOffset && walSize > walHeaderSize {
		seg, err := SealWALSegment(m.walPath(), local.WALOffset)
		if err != nil {
			return res, err
		}
		if len(seg) > 0 {
			if err := m.commitSegment(ctx, st, seg, local.LastSegmentSeq+1); err != nil {
				return res, err
			}
			res.SealedSegments++
			local.LastSegmentSeq++
			local.WALOffset += int64(len(seg))
			if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
				return res, err
			}
		}
	}

	// State commit for docs-only passes (no segment sealed this pass but
	// objects changed): the committed map is already updated in memory.
	if objectsDirty && res.SealedSegments == 0 {
		st.UpdatedAt = m.now().UTC()
		sb, err := MarshalState(st)
		if err != nil {
			return res, err
		}
		if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(NormalizePrefix(m.cfg.Prefix)), sb); err != nil {
			return res, fmt.Errorf("ship: PUT state (objects): %w", err)
		}
	}

	// --- Branches (2)/(3): idle short-circuit + fold rules. Content can
	// ONLY change via the WAL in WAL mode (a checkpoint preserves page
	// content — measured: benign folds leave the db hash identical), so a
	// pass is certain-idle ONLY when the WAL is present with the recorded
	// identity and no growth. Every other case hashes: identity change,
	// post-seal refresh, and the WAL-absent-unknown case (a CLI command's
	// write+close fold leaves no WAL trace — only the hash sees it; that
	// per-command hash is the accepted cost of fold detection on the CLI
	// path). No stat signal observes a checkpoint (db size/mtime, -shm
	// mtime, and the header change counter all stay constant — measured),
	// so none is consulted. ---
	identityChanged := (!walPresent && local.WALSalt != 0) ||
		(walPresent && local.WALSalt != 0 && walHdr.SaltID() != local.WALSalt)
	certainIdle := walPresent && local.WALSalt != 0 && walHdr.SaltID() == local.WALSalt &&
		walSize == local.WALOffset && res.SealedSegments == 0
	if certainIdle {
		return res, nil
	}

	hash, _, err := hashFile(m.dbPath())
	if err != nil {
		return res, fmt.Errorf("ship: hash db: %w", err)
	}
	res.HashedDB = true
	hashDiverged := hash != local.LastDBSHA256

	switch {
	case hashDiverged && (identityChanged || !walPresent):
		// (a) content beyond the committed state with NO WAL chain covering
		// it: checkpoint-on-close folded unsealed frames (identity change)
		// or a write+close fold before any WAL adoption (WAL absent — the
		// normal post-command CLI case) → force rotation, debounced.
		if m.now().Sub(local.LastRotationAt) >= m.cfg.MinRotationInterval {
			if err := m.rotate(ctx, st); err != nil {
				return res, err
			}
			res.Rotated = true
			res.Warnings = m.lastPruneWarnings
			return res, nil
		}
		// Defer: persist pending_rotation, KEEP pre-fold bookkeeping so the
		// next pass re-detects by construction (spec §Close-fold (4)).
		local.PendingRotation = true
		if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
			return res, err
		}
		res.PendingRotation = true
		return res, nil
	case identityChanged && !hashDiverged:
		// (b) benign fold (content unchanged): adopt the new WAL incarnation.
		local.WALSalt, local.WALOffset = m.adoptWAL()
		if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
			return res, err
		}
		return res, nil
	case hashDiverged:
		// (c) identity intact, WAL chain covers the content (post-seal
		// refresh or attach-path drift) — update reference bookkeeping.
		local.LastDBSHA256 = hash
		if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
			return res, err
		}
		return res, nil
	default:
		return res, nil
	}
}

// rotate performs the pinned 4-step generation rotation (spec.md §Rotation):
// tail seal → meta.json PUT → snapshot PUT → state commit, then the atomic
// local commit (step 5).
func (m *Mirror) rotate(ctx context.Context, st *State) error {
	local := m.local
	prefix := NormalizePrefix(m.cfg.Prefix)
	gen := st.Generation

	// 1. Tail seal: ship any remaining WAL frames as gen N's final segment.
	walHdr, walSize, walErr := WALInfoFromFile(m.walPath())
	if walErr == nil && local.WALSalt != 0 && walHdr.SaltID() == local.WALSalt && walSize > local.WALOffset {
		seg, err := SealWALSegment(m.walPath(), local.WALOffset)
		if err != nil {
			return fmt.Errorf("rotate: tail seal: %w", err)
		}
		if len(seg) > 0 {
			if err := m.commitSegment(ctx, st, seg, local.LastSegmentSeq+1); err != nil {
				return fmt.Errorf("rotate: commit tail: %w", err)
			}
			local.LastSegmentSeq++
			local.WALOffset += int64(len(seg))
		}
	}

	// 2. meta.json PUT for the superseded generation (derived FROM the
	// committed mirror-state's wal list — divergence-free by construction).
	now := m.now().UTC()
	meta := &GenerationMeta{
		FormatVersion:  FormatVersion,
		Generation:     gen,
		CreatedAt:      st.DB.CreatedAt,
		SealedAt:       now,
		Snapshot:       st.DB.Snapshot,
		SnapshotSHA256: st.DB.SnapshotSHA256,
		WAL:            st.DB.WAL,
	}
	mb, err := MarshalMeta(meta)
	if err != nil {
		return fmt.Errorf("rotate: marshal meta: %w", err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, GenerationMetaKey(prefix, gen), mb); err != nil {
		return fmt.Errorf("rotate: PUT meta.json: %w", err)
	}

	// 3. Checkpoint RESTART (fresh WAL for the new generation) + snapshot.
	if err := m.checkpointRestart(); err != nil {
		return fmt.Errorf("rotate: checkpoint: %w", err)
	}
	snapBytes, err := snapshotDatabase(ctx, m.dbPath())
	var deferral *DeferredError
	if errors.As(err, &deferral) {
		return err // counter already incremented inside snapshotForRotation
	}
	if err != nil {
		return fmt.Errorf("rotate: snapshot: %w", err)
	}
	compressed, err := zstdEncode(snapBytes)
	if err != nil {
		return fmt.Errorf("rotate: compress: %w", err)
	}
	newSnapKey := SnapshotKey(prefix, gen+1)
	if err := m.client.PutObject(ctx, m.cfg.Bucket, newSnapKey, compressed); err != nil {
		return fmt.Errorf("rotate: PUT snapshot: %w", err)
	}

	// 4. State commit: new generation, empty WAL list, objects carried over.
	newState := &State{
		FormatVersion: FormatVersion,
		Generation:    gen + 1,
		DB: DBState{
			Snapshot:       newSnapKey,
			SnapshotSHA256: sha256HexBytes(compressed),
			CreatedAt:      now,
			WAL:            []WALSegmentRef{},
		},
		Objects:   st.Objects,
		Vectors:   st.Vectors,
		UpdatedAt: now,
	}
	sb, err := MarshalState(newState)
	if err != nil {
		return fmt.Errorf("rotate: marshal state: %w", err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(prefix), sb); err != nil {
		return fmt.Errorf("rotate: PUT state: %w", err)
	}

	// 5. Atomic local commit: bookkeeping + pending_rotation clear + defers
	// reset in ONE write (spec §Close-fold (5)).
	local.Generation = gen + 1
	local.WALSalt, local.WALOffset = m.adoptWAL()
	hash, size, herr := hashFile(m.dbPath())
	if herr == nil {
		local.LastDBSHA256, local.LastDBSize = hash, size
	}
	local.LastSegmentSeq = 0
	local.LastRotationAt = now
	local.PendingRotation = false
	local.ConsecutiveDefers = 0
	if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
		return fmt.Errorf("rotate: local commit: %w", err)
	}
	m.lastPruneWarnings = m.pruneGenerations(ctx, gen+1)
	return nil
}

// commitSegment PUTs a zstd-compressed segment and updates the committed
// state's wal list (segment first, state LAST — write-then-commit).
func (m *Mirror) commitSegment(ctx context.Context, st *State, seg []byte, seq int) error {
	prefix := NormalizePrefix(m.cfg.Prefix)
	compressed, err := zstdEncode(seg)
	if err != nil {
		return err
	}
	key := WALSegmentKey(prefix, st.Generation, seq)
	if err := m.client.PutObject(ctx, m.cfg.Bucket, key, compressed); err != nil {
		return fmt.Errorf("ship: PUT segment: %w", err)
	}
	st.DB.WAL = append(st.DB.WAL, WALSegmentRef{
		Key:      key,
		SHA256:   sha256HexBytes(compressed),
		SealedAt: m.now().UTC(),
	})
	st.UpdatedAt = m.now().UTC()
	sb, err := MarshalState(st)
	if err != nil {
		return err
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(prefix), sb); err != nil {
		return fmt.Errorf("ship: PUT state: %w", err)
	}
	return nil
}

// adoptWAL returns (salt, offset) for the current WAL incarnation: its salt
// and 0 offset when present (fresh WAL, nothing shipped), or (0, 0) when
// absent (post-close fold — adopt on next sighting).
func (m *Mirror) adoptWAL() (uint64, int64) {
	hdr, _, err := WALInfoFromFile(m.walPath())
	if err != nil {
		return 0, 0
	}
	return hdr.SaltID(), 0
}

// checkpointRestart runs a RESTART checkpoint so the new generation's WAL
// starts fresh (new salt, byte 0) — segments stay self-contained.
func (m *Mirror) checkpointRestart() error {
	db, err := sql.Open("sqlite", m.dbPath())
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	_, err = db.Exec("PRAGMA wal_checkpoint(RESTART)")
	return err
}

func (m *Mirror) dbPath() string  { return filepath.Join(m.dir, ".sage", "wiki.db") }
func (m *Mirror) walPath() string { return m.dbPath() + "-wal" }
func (m *Mirror) shmPath() string { return m.dbPath() + "-shm" }

// remoteState loads the committed mirror-state (error if none — enable
// bootstraps it).
func (m *Mirror) remoteState(ctx context.Context) (*State, error) {
	prefix := NormalizePrefix(m.cfg.Prefix)
	sb, err := m.client.GetObject(ctx, m.cfg.Bucket, StateKey(prefix))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return nil, &NotReadyError{Op: "ship (mirror not enabled remotely)"}
		}
		return nil, fmt.Errorf("ship: read remote state: %w", err)
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		return nil, err
	}
	if err := st.Validate(); err != nil {
		return nil, fmt.Errorf("ship: remote state invalid: %w", err)
	}
	return st, nil
}
