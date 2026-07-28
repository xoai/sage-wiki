package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
)

// BatchCheckpoint is the on-disk state of an in-flight LLM batch, stored at
// .sage/batch-state.json. It replaced the Batch/Pending fields of the legacy
// .sage/compile-state.json (P1-3 / REL-06): compile_items is the single
// source of resume truth for standard compiles, and this file is the only
// place batch state lives.
//
// Why a file and not a DB row: batch state is written once at submit and
// read once at resume by the same single-process CLI, must survive a DB
// schema mismatch, and stays inspectable/gitignore-able exactly like the
// legacy file it replaces. Spec: .sage/work/20260720-p1-3-checkpoint-deprecation/spec.md D1.
type BatchCheckpoint struct {
	CompileID string      `json:"compile_id"`
	StartedAt string      `json:"started_at"`
	Batch     *BatchState `json:"batch"`
	Pending   []string    `json:"pending"`
}

// batchCheckpointPath returns the canonical batch checkpoint location.
func batchCheckpointPath(projectDir string) string {
	return filepath.Join(projectDir, ".sage", "batch-state.json")
}

// legacyCheckpointPath returns the legacy (pre-P1-3) checkpoint location.
func legacyCheckpointPath(projectDir string) string {
	return filepath.Join(projectDir, ".sage", "compile-state.json")
}

// loadBatchCheckpoint reads .sage/batch-state.json. A missing file is
// (nil, nil). Corrupt JSON is an error — never treated as absent, because
// silently ignoring an unparseable checkpoint could strand an in-flight
// batch (spec D2).
func loadBatchCheckpoint(projectDir string) (*BatchCheckpoint, error) {
	data, err := readStateFileRetrying(batchCheckpointPath(projectDir), nil)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("compiler.loadBatchCheckpoint: read: %w", err)
	}
	var bcp BatchCheckpoint
	if err := json.Unmarshal(data, &bcp); err != nil {
		return nil, fmt.Errorf("compiler.loadBatchCheckpoint: parse %s: %w", batchCheckpointPath(projectDir), err)
	}
	return &bcp, nil
}

// saveBatchCheckpoint writes .sage/batch-state.json atomically (unique temp
// file + rename). The temp name is randomized via os.CreateTemp: the old
// fixed path+".tmp" pattern let concurrent writers interleave bytes into
// each other's temp file and rename the corrupted result, or abort on a
// rename collision — in the submit path that could orphan a paid batch ID.
func saveBatchCheckpoint(projectDir string, bcp *BatchCheckpoint) error {
	path := batchCheckpointPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(bcp, "", "  ")
	if err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: marshal: %w", err)
	}
	if err := writeFileAtomicUnique(path, data); err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: %w", err)
	}
	return nil
}

// writeFileAtomicUnique writes data to path atomically using a uniquely-named
// temp file in the same directory (concurrent-writer safe) + rename.
func writeFileAtomicUnique(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close: %w", err)
	}
	// Windows CI taught us: renaming over a just-written destination can
	// transiently fail (Defender/indexer/open-handle timing). Retry a
	// bounded, jittered number of times before declaring failure; keep the
	// temp file across attempts and remove it only on final failure.
	var renameErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(10+rand.Intn(20)) * time.Millisecond)
		}
		if renameErr = os.Rename(tmpName, path); renameErr == nil {
			return nil
		}
		if !isTransientRenameError(renameErr) {
			break
		}
	}
	os.Remove(tmpName) // don't orphan the temp file
	return fmt.Errorf("rename: %w", renameErr)
}

// readStateFileRetrying reads a checkpoint file, retrying the transient
// Windows failures that a concurrent writer causes.
//
// Both state files (batch-state.json, compile-state.json) are written through
// writeFileAtomicUnique, which already retries its rename. Reads needed the
// same treatment, and needed it at EVERY site: fixing only the compile-state
// reader moved the CI failure to the batch-state reader rather than curing it.
// One helper, used by both, so a third reader cannot silently reintroduce the
// gap. A missing file is returned as-is so callers' os.IsNotExist checks work.
//
// The reader is injectable so the retry is testable on any OS.
func readStateFileRetrying(path string, read func(string) ([]byte, error)) ([]byte, error) {
	if read == nil {
		read = os.ReadFile
	}
	var data []byte
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(10+rand.Intn(20)) * time.Millisecond)
		}
		data, err = read(path)
		if err == nil {
			return data, nil
		}
		if os.IsNotExist(err) || !isTransientRenameError(err) {
			return nil, err
		}
	}
	return nil, err
}

// isTransientRenameError reports whether a file operation failed for a
// reason worth retrying (another handle is open) vs a persistent one
// (read-only dir, missing parent) that should fail fast.
//
// The message list is the load-bearing part. Windows reports
// ERROR_SHARING_VIOLATION (32) as "The process cannot access the file because
// it is being used by another process." — which contains NEITHER the phrase
// "sharing violation" NOR "access is denied", and which Go does not map to
// fs.ErrPermission (only ERROR_ACCESS_DENIED is mapped). An earlier version of
// this predicate checked only those two phrases plus ErrPermission, so the
// single most common Windows contention error fell straight through as fatal.
// Matching on the human-readable text is unfortunate but portable; the errno
// is not reachable without a Windows-only build file.
func isTransientRenameError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrPermission) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"being used by another process", // ERROR_SHARING_VIOLATION (32)
		"access is denied",              // ERROR_ACCESS_DENIED (5)
		"sharing violation",             // some layers do surface this wording
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// stripLegacyBatch atomically rewrites the legacy compile-state.json with
// Batch set to nil, preserving every other field (Completed/Pending/Failed
// are still needed by MigrateCheckpoint). An absent file is a silent no-op —
// the common case on post-upgrade installs.
//
// The strip is the load-bearing half of the P1-3 migration choreography
// (spec D2/D5): without it, a completed batch resume leaves the legacy batch
// marker behind and the next compile re-materializes batch-state.json from
// it, polling a consumed batch ID. It is called by the split below, by
// resumeBatch's terminal paths, and defensively by MigrateCheckpoint — one
// copy of the choreography, not three.
func stripLegacyBatch(projectDir string) error {
	path := legacyCheckpointPath(projectDir)
	state, err := loadCompileState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("compiler.stripLegacyBatch: load: %w", err)
	}
	if state.Batch == nil {
		return nil // already stripped
	}
	state.Batch = nil
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("compiler.stripLegacyBatch: marshal: %w", err)
	}
	if err := writeFileAtomicUnique(path, data); err != nil {
		return fmt.Errorf("compiler.stripLegacyBatch: %w", err)
	}
	return nil
}

// clearAllCheckpoints removes both checkpoint files (batch + legacy). Used by
// --fresh, whose contract is "start clean" — including the provider-mismatch
// recovery the batch-resume error message promises ("clear checkpoint with
// --fresh"; spec D3). Best-effort: removal failures are logged, not fatal.
func clearAllCheckpoints(projectDir string) {
	for _, p := range []string{batchCheckpointPath(projectDir), legacyCheckpointPath(projectDir)} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Warn("fresh: failed to remove checkpoint", "path", p, "error", err)
		}
	}
}

// retireBatchCheckpoint is the terminal step of every batch resume path
// (spec D5): strip the legacy batch marker (if any), then — only on strip
// success — delete batch-state.json. Deleting unconditionally would let an
// unstripped legacy marker re-materialize a consumed batch on the next run;
// keeping the file on strip failure degrades to a consistent re-poll.
func retireBatchCheckpoint(projectDir string) {
	if err := stripLegacyBatch(projectDir); err != nil {
		log.Warn("retire batch checkpoint: legacy strip failed — keeping batch-state.json for a consistent re-poll", "error", err)
		return
	}
	if err := os.Remove(batchCheckpointPath(projectDir)); err != nil && !os.IsNotExist(err) {
		log.Warn("retire batch checkpoint: remove failed", "error", err)
	}
}

// loadOrMigrateBatchCheckpoint is the batch-resume entry point (spec D2). It
// returns the in-flight batch checkpoint, migrating a legacy
// compile-state.json on first encounter:
//
//  1. batch-state.json exists with Batch != nil → load and return it.
//  2. Else (no file, or a DEAD batch-less file — hand-edit/corruption, since
//     no writer produces one): check the legacy compile-state.json. A dead
//     file must not mask a legacy in-flight batch — without the fall-through,
//     Compile's dead-file removal plus MigrateCheckpoint's defensive strip
//     would silently strand a paid batch (independent-verification finding).
//     Legacy Batch != nil → SPLIT: write the batch portion to
//     batch-state.json FIRST, then strip Batch from the legacy JSON. This
//     order is mandatory — the reverse can lose the batch ID on a crash
//     between the two writes; this order degrades to an idempotent re-split.
//     A failure of either write, or corrupt JSON in either file, is an ERROR
//     (aborts the caller) — never fall through to nil,nil, which could strand
//     the in-flight batch and let MigrateCheckpoint's delete destroy the only
//     copy of the batch ID.
//  3. Legacy has no batch → return whatever step 1 loaded (possibly a dead
//     batch-less file, which Compile removes as dead state; possibly nil).
func loadOrMigrateBatchCheckpoint(projectDir string) (*BatchCheckpoint, error) {
	bcp, err := loadBatchCheckpoint(projectDir)
	if err != nil {
		return nil, err
	}
	if bcp != nil && bcp.Batch != nil {
		return bcp, nil
	}

	state, err := loadCompileState(legacyCheckpointPath(projectDir))
	if err != nil {
		if os.IsNotExist(err) {
			return bcp, nil
		}
		return nil, fmt.Errorf("compiler.loadOrMigrateBatchCheckpoint: load legacy: %w", err)
	}
	if state.Batch == nil {
		return bcp, nil // batch-less legacy checkpoint — MigrateCheckpoint owns it
	}

	bcp = &BatchCheckpoint{
		CompileID: state.CompileID,
		StartedAt: state.StartedAt,
		Batch:     state.Batch,
		Pending:   state.Pending,
	}
	if err := saveBatchCheckpoint(projectDir, bcp); err != nil {
		return nil, fmt.Errorf("compiler.loadOrMigrateBatchCheckpoint: split write: %w", err)
	}
	if err := stripLegacyBatch(projectDir); err != nil {
		return nil, fmt.Errorf("compiler.loadOrMigrateBatchCheckpoint: split strip: %w", err)
	}
	log.Info("migrated in-flight batch from legacy checkpoint",
		"batch_id", state.Batch.BatchID, "provider", state.Batch.Provider)
	return bcp, nil
}

// hasPendingBatch reports whether a pending batch exists in EITHER
// checkpoint file — the canonical .sage/batch-state.json or a legacy
// .sage/compile-state.json with Batch != nil. Shared by watch mode and the
// serve worker (P2-3): batch owns the pipeline until retired, so both
// refuse/idle rather than interleave with a batch resume. Unreadable
// checkpoints warn loudly (a corrupt checkpoint fails every compile it
// meets) and do not count as pending.
func hasPendingBatch(projectDir string) bool {
	if bcp, err := loadBatchCheckpoint(projectDir); err != nil {
		log.Warn("batch checkpoint unreadable — compiles will fail until it is fixed or removed", "error", err)
	} else if bcp != nil && bcp.Batch != nil {
		return true
	}
	if state, err := loadCompileState(legacyCheckpointPath(projectDir)); err != nil && !os.IsNotExist(err) {
		log.Warn("legacy checkpoint unreadable — compiles will fail until it is fixed or removed", "error", err)
	} else if state != nil && state.Batch != nil {
		return true
	}
	return false
}
