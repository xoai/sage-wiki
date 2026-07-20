package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	data, err := os.ReadFile(batchCheckpointPath(projectDir))
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

// saveBatchCheckpoint writes .sage/batch-state.json atomically (temp +
// rename, same pattern the legacy saveCompileState used).
func saveBatchCheckpoint(projectDir string, bcp *BatchCheckpoint) error {
	path := batchCheckpointPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(bcp, "", "  ")
	if err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("compiler.saveBatchCheckpoint: rename: %w", err)
	}
	return nil
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("compiler.stripLegacyBatch: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("compiler.stripLegacyBatch: rename: %w", err)
	}
	return nil
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
//  1. batch-state.json exists → load and return it.
//  2. Else legacy compile-state.json has Batch != nil → SPLIT: write the
//     batch portion to batch-state.json FIRST, then strip Batch from the
//     legacy JSON. This order is mandatory — the reverse can lose the batch
//     ID on a crash between the two writes; this order degrades to an
//     idempotent re-split. A failure of either write, or corrupt JSON in
//     either file, is an ERROR (aborts the caller) — never fall through to
//     nil,nil, which could strand the in-flight batch and let
//     MigrateCheckpoint's delete destroy the only copy of the batch ID.
//  3. Else → nil, nil.
func loadOrMigrateBatchCheckpoint(projectDir string) (*BatchCheckpoint, error) {
	bcp, err := loadBatchCheckpoint(projectDir)
	if err != nil {
		return nil, err
	}
	if bcp != nil {
		return bcp, nil
	}

	state, err := loadCompileState(legacyCheckpointPath(projectDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("compiler.loadOrMigrateBatchCheckpoint: load legacy: %w", err)
	}
	if state.Batch == nil {
		return nil, nil // batch-less legacy checkpoint — MigrateCheckpoint owns it
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
