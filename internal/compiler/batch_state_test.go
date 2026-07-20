package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBatchCheckpointRoundtrip(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".sage"), 0755); err != nil {
		t.Fatal(err)
	}

	original := &BatchCheckpoint{
		CompileID: "20260720-120000",
		StartedAt: "2026-07-20T12:00:00Z",
		Batch: &BatchState{
			BatchID:     "batch_xyz",
			Provider:    "anthropic",
			Pass:        "summarize",
			ResultsRef:  "https://example.com/results",
			SubmittedAt: "2026-07-20T12:00:00Z",
			PathByID:    map[string]string{"abc123": "raw/a.md", "def456": "raw/b.md"},
		},
		Pending: []string{"raw/a.md", "raw/b.md"},
	}

	if err := saveBatchCheckpoint(projectDir, original); err != nil {
		t.Fatalf("saveBatchCheckpoint: %v", err)
	}

	loaded, err := loadBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadBatchCheckpoint: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected checkpoint, got nil")
	}
	if loaded.CompileID != original.CompileID {
		t.Errorf("CompileID = %q, want %q", loaded.CompileID, original.CompileID)
	}
	if loaded.StartedAt != original.StartedAt {
		t.Errorf("StartedAt = %q, want %q", loaded.StartedAt, original.StartedAt)
	}
	if loaded.Batch == nil {
		t.Fatal("Batch is nil")
	}
	if loaded.Batch.BatchID != "batch_xyz" || loaded.Batch.Provider != "anthropic" || loaded.Batch.Pass != "summarize" {
		t.Errorf("Batch = %+v, want ID batch_xyz / anthropic / summarize", loaded.Batch)
	}
	if loaded.Batch.ResultsRef != original.Batch.ResultsRef {
		t.Errorf("ResultsRef = %q, want %q", loaded.Batch.ResultsRef, original.Batch.ResultsRef)
	}
	if len(loaded.Batch.PathByID) != 2 || loaded.Batch.PathByID["abc123"] != "raw/a.md" {
		t.Errorf("PathByID = %v, want 2 entries incl abc123->raw/a.md", loaded.Batch.PathByID)
	}
	if len(loaded.Pending) != 2 || loaded.Pending[0] != "raw/a.md" {
		t.Errorf("Pending = %v, want [raw/a.md raw/b.md]", loaded.Pending)
	}
}

func TestLoadBatchCheckpointAbsent(t *testing.T) {
	projectDir := t.TempDir()

	loaded, err := loadBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadBatchCheckpoint on absent file: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil for absent file, got %+v", loaded)
	}
}

func TestLoadBatchCheckpointCorrupt(t *testing.T) {
	projectDir := t.TempDir()
	sageDir := filepath.Join(projectDir, ".sage")
	if err := os.MkdirAll(sageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sageDir, "batch-state.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	// Corrupt JSON must error — treating it as absent could strand an
	// in-flight batch (spec D2: abort, never treat-as-absent).
	loaded, err := loadBatchCheckpoint(projectDir)
	if err == nil {
		t.Errorf("expected error for corrupt JSON, got nil (loaded=%+v)", loaded)
	}
}

func TestSaveBatchCheckpointAtomic(t *testing.T) {
	projectDir := t.TempDir()

	// .sage does not exist yet — save must create it.
	bcp := &BatchCheckpoint{CompileID: "x", Batch: &BatchState{BatchID: "b1"}}
	if err := saveBatchCheckpoint(projectDir, bcp); err != nil {
		t.Fatalf("saveBatchCheckpoint: %v", err)
	}
	// No temp file should linger after a successful save.
	if _, err := os.Stat(filepath.Join(projectDir, ".sage", "batch-state.json.tmp")); !os.IsNotExist(err) {
		t.Error("temp file lingered after save")
	}
}

func writeLegacyState(t *testing.T, projectDir string, state CompileState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".sage"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".sage", "compile-state.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
}

func readLegacyState(t *testing.T, projectDir string) *CompileState {
	t.Helper()
	state, err := loadCompileState(legacyCheckpointPath(projectDir))
	if err != nil {
		t.Fatalf("loadCompileState: %v", err)
	}
	return state
}

func TestLoadOrMigrateBatchCheckpoint_Split(t *testing.T) {
	projectDir := t.TempDir()
	writeLegacyState(t, projectDir, CompileState{
		CompileID: "20260719-090000",
		StartedAt: "2026-07-19T09:00:00Z",
		Pass:      1,
		Completed: []string{"raw/old.md"},
		Pending:   []string{"raw/a.md", "raw/b.md"},
		Failed:    []FailedSource{{Path: "raw/f.md", Error: "rate limited", Attempts: 2}},
		Batch: &BatchState{
			BatchID:  "batch_legacy",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{"id1": "raw/a.md"},
		},
	})

	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadOrMigrateBatchCheckpoint: %v", err)
	}
	if bcp == nil || bcp.Batch == nil {
		t.Fatal("expected migrated batch checkpoint")
	}
	if bcp.Batch.BatchID != "batch_legacy" || bcp.Batch.Provider != "openai" {
		t.Errorf("Batch = %+v, want batch_legacy/openai", bcp.Batch)
	}
	if len(bcp.Pending) != 2 {
		t.Errorf("Pending = %v, want 2 entries", bcp.Pending)
	}
	if bcp.CompileID != "20260719-090000" || bcp.StartedAt != "2026-07-19T09:00:00Z" {
		t.Errorf("IDs = %q/%q, want 20260719-090000/2026-07-19T09:00:00Z", bcp.CompileID, bcp.StartedAt)
	}

	// batch-state.json written on disk
	if _, err := os.Stat(batchCheckpointPath(projectDir)); err != nil {
		t.Errorf("batch-state.json not written: %v", err)
	}

	// Legacy JSON rewritten with Batch STRIPPED, other fields intact — a
	// completed resume must never re-materialize the batch from this file.
	legacy := readLegacyState(t, projectDir)
	if legacy.Batch != nil {
		t.Error("legacy JSON still has Batch after split — strip is mandatory")
	}
	if len(legacy.Completed) != 1 || len(legacy.Pending) != 2 || len(legacy.Failed) != 1 {
		t.Errorf("legacy fields lost: completed=%v pending=%v failed=%v",
			legacy.Completed, legacy.Pending, legacy.Failed)
	}
}

func TestLoadOrMigrateBatchCheckpoint_ExistingBatchFileWins(t *testing.T) {
	projectDir := t.TempDir()
	// Pre-existing batch-state.json (newer) + legacy JSON with an OLDER batch.
	writeLegacyState(t, projectDir, CompileState{
		Batch: &BatchState{BatchID: "batch_old", Provider: "openai"},
	})
	if err := saveBatchCheckpoint(projectDir, &BatchCheckpoint{
		Batch:   &BatchState{BatchID: "batch_new", Provider: "openai"},
		Pending: []string{"raw/x.md"},
	}); err != nil {
		t.Fatal(err)
	}

	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadOrMigrateBatchCheckpoint: %v", err)
	}
	if bcp.Batch.BatchID != "batch_new" {
		t.Errorf("BatchID = %q, want batch_new (existing file must win)", bcp.Batch.BatchID)
	}
	// Legacy untouched — MigrateCheckpoint owns its remaining fields.
	legacy := readLegacyState(t, projectDir)
	if legacy.Batch == nil || legacy.Batch.BatchID != "batch_old" {
		t.Error("legacy JSON modified even though batch-state.json existed")
	}
}

func TestLoadOrMigrateBatchCheckpoint_LegacyNoBatch(t *testing.T) {
	projectDir := t.TempDir()
	writeLegacyState(t, projectDir, CompileState{
		CompileID: "c1",
		Pass:      2,
		Completed: []string{"raw/a.md"},
	})

	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadOrMigrateBatchCheckpoint: %v", err)
	}
	if bcp != nil {
		t.Errorf("expected nil for batch-less legacy JSON, got %+v", bcp)
	}
	if _, err := os.Stat(batchCheckpointPath(projectDir)); !os.IsNotExist(err) {
		t.Error("batch-state.json should not be created for batch-less legacy JSON")
	}
}

func TestLoadOrMigrateBatchCheckpoint_None(t *testing.T) {
	projectDir := t.TempDir()
	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadOrMigrateBatchCheckpoint: %v", err)
	}
	if bcp != nil {
		t.Errorf("expected nil,nil, got %+v", bcp)
	}
}

func TestLoadOrMigrateBatchCheckpoint_CorruptLegacy(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".sage"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(projectDir, ".sage", "compile-state.json"), []byte("{broken"), 0644)

	// Corrupt legacy JSON aborts — treating it as absent could strand an
	// in-flight batch (spec D2).
	if _, err := loadOrMigrateBatchCheckpoint(projectDir); err == nil {
		t.Error("expected error for corrupt legacy JSON")
	}
}

func TestStripLegacyBatch(t *testing.T) {
	projectDir := t.TempDir()
	writeLegacyState(t, projectDir, CompileState{
		CompileID: "c1",
		Completed: []string{"raw/a.md"},
		Batch:     &BatchState{BatchID: "b1"},
	})

	if err := stripLegacyBatch(projectDir); err != nil {
		t.Fatalf("stripLegacyBatch: %v", err)
	}
	legacy := readLegacyState(t, projectDir)
	if legacy.Batch != nil {
		t.Error("Batch not stripped")
	}
	if legacy.CompileID != "c1" || len(legacy.Completed) != 1 {
		t.Errorf("other fields lost: %+v", legacy)
	}

	// Second call is a no-op (already stripped).
	if err := stripLegacyBatch(projectDir); err != nil {
		t.Fatalf("stripLegacyBatch second call: %v", err)
	}
}

func TestStripLegacyBatchAbsentIsNoOp(t *testing.T) {
	projectDir := t.TempDir()
	if err := stripLegacyBatch(projectDir); err != nil {
		t.Errorf("absent legacy file must be a silent no-op, got %v", err)
	}
}
