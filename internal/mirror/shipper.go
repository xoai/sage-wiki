package mirror

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/pkg/events"
	"time"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// shipResult reports one ship pass (best-effort; callers warn on error,
// never fail the invoking command).
type shipResult struct {
	SealedSegments     int
	BytesShipped       int64 // WAL bytes sealed into segments this pass
	Rotated            bool
	PendingRotation    bool
	HashedDB           bool // the full db hash ran this pass (cost observability)
	ObjectsShipped     int
	ObjectsTombstoned  int
	ObjectsResurrected int
	Warnings           []string
}

// withOwnBudget returns a ctx with its own timeout that ALSO cancels when
// parent does (a plain WithTimeout(parent) can't grow the budget).
func withOwnBudget(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// reconcileWalBookkeeping runs pass-entry wal reconciliation (Gate-3
// F-077/F-078): local wal bookkeeping must agree with the committed remote
// chain BEFORE any sealing. shipPass AND Snapshot both run it (pass-3
// F-114: a rotation without it re-seals the committed tail into a
// duplicate seq and wedges the remote permanently).
func (m *Mirror) reconcileWalBookkeeping(ctx context.Context, res *shipResult, st *State, local *LocalState) error {
	walHdr, _, walErr := WALInfoFromFile(m.walPath())
	walPresent := walErr == nil
	if walErr != nil && !errors.Is(walErr, os.ErrNotExist) {
		var torn *ErrTornWALHeader
		if !errors.As(walErr, &torn) {
			return walErr
		}
	}
	// (see caller comment)
	// bookkeeping must agree with the committed remote chain BEFORE any
	// sealing, on EVERY pass — not only under pending_rotation. The two
	// crash windows: (i) tail committed remotely but the local write lost
	// (same generation, stale seq/offset); (ii) rotation step 4 committed
	// remotely but step 5 lost (generation mismatch, and the on-disk WAL
	// may still hold pre-restart content that must never seal into the new
	// generation). ---
	if local.Generation != st.Generation {
		// (ii): generation mismatch (rotation step 4 committed remotely,
		// step 5 lost). The on-disk WAL may be (a) the PRE-restart
		// incarnation (its content is already in the snapshot — sealing it
		// would poison the new chain with a second salt), or (b) a
		// POST-restart incarnation (frames written after the crashed
		// rotation's checkpoint — they LEGITIMATELY belong to the new
		// chain and must seal from byte 0 with their header). Distinguish
		// by salt against the stale bookkeeping; checkpointRestart ONLY in
		// case (a)/unknown, and re-read identity AFTER it — pass-entry
		// reads are stale by then (F-093).
		staleSalt := local.WALSalt
		staleGen := local.Generation
		curHdr, _, curErr := WALInfoFromFile(m.walPath())
		local.Generation = st.Generation
		local.LastSegmentSeq = len(st.DB.WAL)
		switch {
		case curErr == nil && staleSalt != 0 && curHdr.SaltID() != staleSalt:
			// (b): adopt the fresh incarnation at offset 0 — the caller's
			// seal branch re-reads identity and seals from the header; the
			// chain keeps ONE salt.
			local.WALSalt = curHdr.SaltID()
			local.WALOffset = 0
		default:
			// (a)/unknown: fold the stale incarnation (its content is
			// either already in the snapshot or handled by the fold rules
			// below), then adopt whatever fresh WAL exists.
			if err := m.checkpointRestart(); err != nil {
				return fmt.Errorf("ship: reconcile generation %d→%d: %w", staleGen, st.Generation, err)
			}
			local.WALSalt, local.WALOffset = m.adoptWAL()
		}
		// A pending rotation IS this remote commit — consume it here.
		if local.PendingRotation {
			local.PendingRotation = false
			local.ConsecutiveDefers = 0
			local.LastRotationAt = st.DB.CreatedAt
		}
		// Refresh the hash reference for the adopted generation.
		if hash, _, herr := hashFile(m.dbPath()); herr == nil {
			local.LastDBSHA256 = hash
			res.HashedDB = true
		}
		if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
			return err
		}
	} else if local.LastSegmentSeq != len(st.DB.WAL) && walPresent && walHdr.SaltID() == local.WALSalt {
		// (i): the remote chain has segments our bookkeeping lost. Realign
		// EXACTLY: the next unsealed byte is the current offset plus the
		// uncompressed lengths of the segments we missed — NOT walSize
		// (frames appended between crash and recovery live there and must
		// seal as the next seq — F-094), and NOT a re-seal of the tail
		// (a duplicated frame breaks WAL's cumulative checksums and
		// poisons everything after it — measured).
		extra, err := m.chainTailLength(ctx, st, local.LastSegmentSeq)
		if err != nil {
			return fmt.Errorf("ship: realign offset: %w", err)
		}
		local.WALOffset += extra
		local.LastSegmentSeq = len(st.DB.WAL)
		if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
			return err
		}
	}
	return nil
}

// Ship implements the pkg seam: a ChangeBatch is accepted for interface
// conformance; shipping is diff-driven (spec.md §Ship trigger), so the batch
// payload is advisory only. Warnings are LOGGED (pass-2 MAJOR-4: prune
// advisories and deferrals must reach an operator, not just the result
// struct that production callers discard).
func (o *mirrorOps) Ship(ctx context.Context, batch pkmirror.ChangeBatch) error {
	res, err := o.m.shipPass(ctx)
	for _, w := range res.Warnings {
		slog.Warn("mirror ship", "warning", w)
	}
	if err == nil {
		o.m.emitShipped(res)
	}
	return err
}

// ShipPass exposes the result for callers that surface warnings themselves
// (serve drain).
func (m *Mirror) ShipPass(ctx context.Context) error {
	res, err := m.shipPass(ctx)
	if err == nil {
		m.emitShipped(res)
	}
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
	local, err := LoadLocalState(localStatePath(m.dir))
	if err != nil {
		return "", err
	}
	m.local = local
	st, err := m.remoteState(ctx)
	if err != nil {
		return "", err
	}
	// F-114: Snapshot rotates directly — it MUST run the same pass-entry
	// reconciliation or a step-1-crash window wedges the remote with a
	// duplicate seq (permanent, bucket surgery).
	var res shipResult
	if err := m.reconcileWalBookkeeping(ctx, &res, st, local); err != nil {
		return "", err
	}
	if err := m.rotate(ctx, st); err != nil {
		return "", err
	}
	m.emitSnapshot(int64(st.Generation + 1))
	return pkmirror.SnapshotID(fmt.Sprintf("generation-%d", st.Generation+1)), nil
}

// shipPass is the single-leader ship cycle (spec.md §Close-fold handling,
// steps in order): reconcile → seal → short-circuit → fold rules →
// debounce/rotation → atomic local commit. Local state is RELOADED under
// the mutex: concurrent processes share it through the file, and the mutex
// is what serializes both the remote and that file.
func (m *Mirror) shipPass(ctx context.Context) (shipResult, error) {
	var res shipResult
	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return res, err
	}
	defer mutex.Release()

	local, err := LoadLocalState(localStatePath(m.dir))
	if err != nil {
		return res, err
	}
	m.local = local

	st, err := m.remoteState(ctx)
	if err != nil {
		// First-pass bootstrap (F-104, pass-2 finding: enable ran pre-db and
		// only wrote the manifest — the first pass with a database MUST
		// bootstrap generation 1, not warn forever).
		var nr *NotReadyError
		if !errors.As(err, &nr) {
			return res, err
		}
		if _, serr := os.Stat(m.dbPath()); serr != nil {
			return res, err // still no db — nothing to bootstrap
		}
		// The bootstrap (snapshot + uploads) gets its OWN budget (pass-4
		// N2): the CLI hook's 2×ship_lock_timeout pass budget can't fit a
		// large-db snapshot and would wedge forever. Parent cancellation is
		// still honored.
		bctx, bcancel := withOwnBudget(ctx, 10*m.cfg.ShipLockTimeout)
		berr := m.bootstrapGeneration1Locked(bctx, "ship bootstrap")
		bcancel()
		if berr != nil {
			return res, berr
		}
		st, err = m.remoteState(ctx)
		if err != nil {
			return res, err
		}
	}

	// WAL identity for reconciliation and sealing. A torn header (kill
	// mid-WAL-creation) reads as ABSENT — SQLite recovery ignores it too.
	walHdr, walSize, walErr := WALInfoFromFile(m.walPath())
	walPresent := walErr == nil
	if walErr != nil && !errors.Is(walErr, os.ErrNotExist) {
		var torn *ErrTornWALHeader
		if !errors.As(walErr, &torn) {
			return res, walErr
		}
	}

	// --- Pass-entry reconciliation (shared with Snapshot, F-114) ---
	if err := m.reconcileWalBookkeeping(ctx, &res, st, local); err != nil {
		return res, err
	}
	// Refresh identity post-reconciliation (adoption/checkpoint may have
	// changed it).
	walHdr, walSize, walErr = WALInfoFromFile(m.walPath())
	walPresent = walErr == nil
	if walErr != nil && !errors.Is(walErr, os.ErrNotExist) {
		var torn *ErrTornWALHeader
		if !errors.As(walErr, &torn) {
			return res, walErr
		}
	}

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
			m.emitSnapshot(int64(st.Generation + 1))
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
		objectsDirty = res.ObjectsShipped+res.ObjectsTombstoned+res.ObjectsResurrected > 0
	}

	// --- WAL adoption: unknown incarnation (post-bootstrap/rotation/fold)
	// → adopt the current WAL's salt with offset 0 (its frames are
	// generation-start content, sealed below from byte 0 incl. header).
	if walPresent && local.WALSalt == 0 {
		local.WALSalt = walHdr.SaltID()
		local.WALOffset = 0
	}

	// --- Branch (1): seal new WAL frames at the recorded (salt, offset) ---
	if walPresent && walHdr.SaltID() == local.WALSalt && walSize > local.WALOffset && walSize > walHeaderSize {
		seg, err := SealWALSegment(m.walPath(), local.WALOffset, local.WALSalt)
		if err != nil {
			var mism *ErrSaltMismatch
			if errors.As(err, &mism) {
				// TOCTOU (F-101): the WAL reset mid-pass — skip sealing;
				// the next pass re-reads identity and classifies the fold.
				res.Warnings = append(res.Warnings, "wal incarnation changed mid-pass; seal deferred to next pass")
				return res, nil
			}
			return res, err
		}
		if len(seg) > 0 {
			if err := m.commitSegment(ctx, st, seg, local.LastSegmentSeq+1); err != nil {
				return res, err
			}
			res.SealedSegments++
			res.BytesShipped += int64(len(seg))
			local.LastSegmentSeq++
			local.WALOffset += int64(len(seg))
			if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
				return res, err
			}
		}
	}

	// Quiesce-refresh happens ONLY in the serve drain (MirrorShipper.Stop
	// via Mirror.Quiesce) — a checkpoint here would force every subsequent
	// write into a fresh WAL incarnation and break chain continuity
	// (measured). See serve/mirror.go Stop.

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
			m.emitSnapshot(int64(st.Generation + 1))
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
		seg, err := SealWALSegment(m.walPath(), local.WALOffset, local.WALSalt)
		if err != nil {
			// Salt mismatch (F-101): fold landed mid-rotation — abort; the
			// next pass re-detects and retries (rotation is idempotent).
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
	// SealedAt is DETERMINISTIC (F-091): a crash-recovery re-run re-derives
	// identical bytes. It is max(tail segment's sealed_at, state's last
	// commit time) — docs-only commits advance UpdatedAt WITHOUT a segment,
	// and the sealed object map is fresh through that commit (pass-4: the
	// skew note's heuristic must match the map's actual freshness). In the
	// WAL-only-tail window (last commits are segments) the note can
	// OVER-report skew for T just before the last doc change — conservative
	// by design, never a false negative.
	now := m.now().UTC()
	sealedAt := st.UpdatedAt
	if n := len(st.DB.WAL); n > 0 && st.DB.WAL[n-1].SealedAt.After(sealedAt) {
		sealedAt = st.DB.WAL[n-1].SealedAt
	}
	meta := &GenerationMeta{
		FormatVersion:  FormatVersion,
		Generation:     gen,
		CreatedAt:      st.DB.CreatedAt,
		SealedAt:       sealedAt,
		Snapshot:       st.DB.Snapshot,
		SnapshotSHA256: st.DB.SnapshotSHA256,
		WAL:            st.DB.WAL,
		Objects:        st.Objects,
		Vectors:        st.Vectors,
	}
	mb, err := MarshalMeta(meta)
	if err != nil {
		return fmt.Errorf("rotate: marshal meta: %w", err)
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, GenerationMetaKey(prefix, gen), mb); err != nil {
		return fmt.Errorf("rotate: PUT meta.json: %w", err)
	}

	// 3. Checkpoint RESTART (fresh WAL for the new generation) + snapshot.
	// A busy writer defers the rotation HERE with the same accounting as
	// the VACUUM fallback — exactly once per deferred attempt (F-079).
	if err := m.checkpointRestart(); err != nil {
		local.ConsecutiveDefers++
		if serr := SaveLocalState(localStatePath(m.dir), local); serr != nil {
			return fmt.Errorf("rotate: persist defer counter: %w", serr)
		}
		return &DeferredError{Reason: "checkpoint: " + err.Error()}
	}
	// The defer counter lives HERE (F-079): each deferred rotation attempt
	// increments and persists consecutive_defers exactly once.
	snapBytes, err := snapshotForRotation(ctx, m.dbPath(), snapOptions{
		busyTimeout: 5 * time.Second,
		maxRetries:  3,
		local:       local,
		localPath:   localStatePath(m.dir),
	})
	m.lastSnapshotBytes.Store(int64(len(snapBytes)))
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
	snapShippedSHA, err := m.putObjectShasum(ctx, newSnapKey, compressed)
	if err != nil {
		return fmt.Errorf("rotate: PUT snapshot: %w", err)
	}

	// 4. State commit: new generation, empty WAL list, objects carried over.
	newState := &State{
		FormatVersion: FormatVersion,
		Generation:    gen + 1,
		DB: DBState{
			Snapshot:       newSnapKey,
			SnapshotSHA256: snapShippedSHA,
			CreatedAt:      now,
			WAL:            []WALSegmentRef{},
		},
		Objects:           st.Objects,
		Vectors:           st.Vectors,
		UpdatedAt:         now,
		RetainGenerations: m.cfg.RetainGenerations,
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
	shippedSHA, err := m.putObjectShasum(ctx, key, compressed)
	if err != nil {
		return fmt.Errorf("ship: PUT segment: %w", err)
	}
	st.DB.WAL = append(st.DB.WAL, WALSegmentRef{
		Key:      key,
		SHA256:   shippedSHA,
		SealedAt: m.now().UTC(),
	})
	st.UpdatedAt = m.now().UTC()
	st.RetainGenerations = m.cfg.RetainGenerations
	sb, err := MarshalState(st)
	if err != nil {
		return err
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, StateKey(prefix), sb); err != nil {
		return fmt.Errorf("ship: PUT state: %w", err)
	}
	return nil
}

// chainTailLength returns the total UNCOMPRESSED byte length of the chain's
// segments from index fromSeq onward (decrypting when needed) — the exact
// realign distance for lost bookkeeping.
func (m *Mirror) chainTailLength(ctx context.Context, st *State, fromSeq int) (int64, error) {
	// Loud, never a panic (F-099/PB-1): local bookkeeping AHEAD of the
	// remote chain means bucket rollback, manual deletion, or a split-brain
	// second writer — a wrong realign would corrupt the chain silently.
	if fromSeq > len(st.DB.WAL) {
		return 0, fmt.Errorf("ship: local seq %d ahead of remote chain length %d (bucket rollback or split-brain — manual inspection required)", fromSeq, len(st.DB.WAL))
	}
	var total int64
	for _, seg := range st.DB.WAL[fromSeq:] {
		b, err := m.client.GetObject(ctx, m.cfg.Bucket, seg.Key)
		if err != nil {
			return 0, fmt.Errorf("download %s: %w", seg.Key, err)
		}
		if m.encKey != nil {
			b, err = decryptBytes(m.encKey, b)
			if err != nil {
				return 0, fmt.Errorf("decrypt %s: %w", seg.Key, err)
			}
		}
		raw, err := zstdDecode(b)
		if err != nil {
			return 0, fmt.Errorf("decompress %s: %w", seg.Key, err)
		}
		total += int64(len(raw))
	}
	return total, nil
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

// Quiesce folds sealed frames into the db (passive checkpoint) and
// refreshes the hash reference — called by the serve drain (Stop), where
// the process is exiting and the next incarnation is naturally fresh.
// After Quiesce, a serve-stop close-fold hashes IDENTICAL to the
// reference (branch (b) benign) instead of a spurious (a) rotation
// (F-102). Runs UNDER the ship-mutex, returns failures loudly, and the
// hash is interruptible via ctx (item 4: an exhausted drain budget aborts
// the hash — reference NOT updated, all-or-nothing).
func (m *Mirror) Quiesce(ctx context.Context) error {
	// Fail fast on an exhausted budget BEFORE the (uninterruptible) mutex
	// acquire — the residual overrun is one acquire timeout, documented
	// (F-029).
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("quiesce: %w", err)
	}
	mutex, err := AcquireShipMutex(m.dir, m.cfg.ShipLockTimeout)
	if err != nil {
		return fmt.Errorf("quiesce: %w", err)
	}
	defer mutex.Release()

	local, err := LoadLocalState(localStatePath(m.dir))
	if err != nil {
		return fmt.Errorf("quiesce: load local state: %w", err)
	}
	db, err := sql.Open("sqlite", m.dbPath())
	if err != nil {
		return fmt.Errorf("quiesce: open db: %w", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=2000"); err != nil {
		return fmt.Errorf("quiesce: busy_timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return fmt.Errorf("quiesce: checkpoint: %w", err)
	}
	hash, size, err := hashFileCtx(ctx, m.dbPath())
	if err != nil {
		return fmt.Errorf("quiesce: hash db: %w", err)
	}
	local.LastDBSHA256 = hash
	local.LastDBSize = size
	if err := SaveLocalState(localStatePath(m.dir), local); err != nil {
		return fmt.Errorf("quiesce: save local state: %w", err)
	}
	return nil
}

// ctxReader aborts a long read when ctx is done (per Read — io.Copy uses
// ≤32KiB chunks, so cancellation is responsive).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// hashFileCtx streams a file's SHA-256 and size, interruptible via ctx.
func hashFileCtx(ctx context.Context, path string) (sha string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, &ctxReader{ctx: ctx, r: f})
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
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

// emitShipped fans one mirror_shipped event per successful ship pass
// (SPEC-07). Generation is the local generation after the pass; Bytes the
// WAL bytes sealed into segments on this pass (0 = nothing new shipped).
func (m *Mirror) emitShipped(res shipResult) {
	if m.sink == nil {
		return
	}
	gen := int64(0)
	if m.local != nil {
		gen = int64(m.local.Generation)
	}
	m.sink.Emit(events.NewEvent(filepath.Base(m.dir), events.TypeMirrorShipped, events.MirrorShipped{
		Generation: gen,
		Bytes:      res.BytesShipped,
	}))
}

// emitSnapshot fans one mirror_snapshot event after a generation rotation.
// Bytes carries the size rotate() recorded for the snapshot it just PUT.
func (m *Mirror) emitSnapshot(generation int64) {
	if m.sink == nil {
		return
	}
	m.sink.Emit(events.NewEvent(filepath.Base(m.dir), events.TypeMirrorSnapshot, events.MirrorSnapshot{
		Generation: generation,
		Bytes:      m.lastSnapshotBytes.Load(),
	}))
}
