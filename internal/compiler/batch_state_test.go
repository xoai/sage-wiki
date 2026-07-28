package compiler

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
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

// TestLoadOrMigrateBatchCheckpoint_DeadFileDoesNotMaskLegacyBatch: a
// batch-less batch-state.json (hand-edit/corruption — no writer produces
// one) must NOT mask a legacy in-flight batch: the legacy batch is rescued
// (split overwrites the dead file), not stranded (verification finding 1).
func TestLoadOrMigrateBatchCheckpoint_DeadFileDoesNotMaskLegacyBatch(t *testing.T) {
	projectDir := t.TempDir()

	// Dead current-format file (Batch == nil)...
	if err := saveBatchCheckpoint(projectDir, &BatchCheckpoint{CompileID: "dead"}); err != nil {
		t.Fatal(err)
	}
	// ...plus a legacy in-flight batch.
	writeLegacyState(t, projectDir, CompileState{
		CompileID: "legacy-1",
		Pending:   []string{"raw/a.md"},
		Batch:     &BatchState{BatchID: "batch_rescue", Provider: "openai", Pass: "summarize"},
	})

	bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
	if err != nil {
		t.Fatalf("loadOrMigrateBatchCheckpoint: %v", err)
	}
	if bcp == nil || bcp.Batch == nil || bcp.Batch.BatchID != "batch_rescue" {
		t.Fatalf("legacy batch was masked by the dead file: bcp=%+v", bcp)
	}

	// The dead file is overwritten with the rescued batch; legacy stripped.
	onDisk, err := loadBatchCheckpoint(projectDir)
	if err != nil || onDisk == nil || onDisk.Batch == nil || onDisk.Batch.BatchID != "batch_rescue" {
		t.Errorf("batch-state.json not overwritten with rescued batch: %+v", onDisk)
	}
	legacy := readLegacyState(t, projectDir)
	if legacy.Batch != nil {
		t.Error("legacy JSON not Batch-stripped after rescue")
	}
}

// TestBatchCheckpointWriters_Concurrent: concurrent writers must not
// spuriously abort on a shared tmp filename (verification finding 2 —
// inherited from the old saveCompileState fixed-name pattern).
func TestBatchCheckpointWriters_Concurrent(t *testing.T) {
	projectDir := t.TempDir()
	writeLegacyState(t, projectDir, CompileState{
		CompileID: "c1",
		Pending:   []string{"raw/a.md"},
		Batch:     &BatchState{BatchID: "batch_race", Provider: "openai"},
	})

	const goroutines = 8
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Race the split AND direct saves — both writer paths.
			if _, err := loadOrMigrateBatchCheckpoint(projectDir); err != nil {
				errs <- err
			}
			if err := saveBatchCheckpoint(projectDir, &BatchCheckpoint{
				Batch: &BatchState{BatchID: "batch_race"},
			}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent writer aborted spuriously: %v", err)
	}

	// Final state consistent: valid batch file, no orphan tmp files.
	if _, err := loadBatchCheckpoint(projectDir); err != nil {
		t.Errorf("final batch-state.json invalid: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(projectDir, ".sage", "*.tmp"))
	if len(matches) != 0 {
		t.Errorf("orphan tmp files: %v", matches)
	}
}

// TestLoadCompileStateRetriesTransientReads pins the read half of the Windows
// file-sharing contract. The write half already retries (writeFileAtomicUnique
// + isTransientRenameError); reads had no equivalent, so a concurrent writer
// holding the handle made loadCompileState fail outright and aborted the
// caller. Observed on windows-latest as TestBatchCheckpointWriters_Concurrent:
// "stripLegacyBatch: load: open ...compile-state.json: The process cannot
// access the file because it is being used by another process."
func TestLoadCompileStateRetriesTransientReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compile-state.json")
	if err := os.WriteFile(path, []byte(`{"completed":["a.md"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	sharing := &fs.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	calls := 0
	read := func(p string) ([]byte, error) {
		calls++
		if calls <= 2 { // two transient sharing violations, then success
			return nil, sharing
		}
		return os.ReadFile(p)
	}

	state, err := loadCompileStateWith(path, read)
	if err != nil {
		t.Fatalf("transient read errors must be retried, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts (2 transient + 1 success), got %d", calls)
	}
	if len(state.Completed) != 1 || state.Completed[0] != "a.md" {
		t.Errorf("state not parsed after retry: %+v", state)
	}
}

func TestLoadCompileStateDoesNotRetryPersistentErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compile-state.json")

	calls := 0
	read := func(p string) ([]byte, error) {
		calls++
		return nil, &fs.PathError{Op: "open", Path: p, Err: os.ErrNotExist}
	}

	_, err := loadCompileStateWith(path, read)
	if !os.IsNotExist(err) {
		t.Errorf("a missing file must surface as NotExist, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("missing file must not be retried, got %d attempts", calls)
	}
}

func TestLoadCompileStateGivesUpOnPersistentSharingViolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compile-state.json")
	calls := 0
	read := func(string) ([]byte, error) {
		calls++
		return nil, &fs.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	if _, err := loadCompileStateWith(path, read); err == nil {
		t.Fatal("a permanently locked file must still fail")
	}
	if calls < 2 {
		t.Errorf("expected several attempts before giving up, got %d", calls)
	}
}

// TestIsTransientRenameErrorMatchesRealWindowsText pins the predicate against
// the EXACT strings Windows emits, not synthetic stand-ins.
//
// This test exists because an earlier fix used os.ErrPermission as its input
// and passed, while the real CI error — ERROR_SHARING_VIOLATION, whose text is
// "The process cannot access the file because it is being used by another
// process." — matched nothing and fell through as fatal. A predicate that
// matches on message text must be tested with the messages that occur.
func TestIsTransientRenameErrorMatchesRealWindowsText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"ERROR_SHARING_VIOLATION as Windows words it",
			errors.New(`open C:\Users\RUNNER~1\AppData\Local\Temp\x\.sage\compile-state.json: ` +
				`The process cannot access the file because it is being used by another process.`),
			true,
		},
		{
			"ERROR_ACCESS_DENIED as Windows words it",
			errors.New(`rename C:\x\tmp C:\x\dst: Access is denied.`),
			true,
		},
		{"literal sharing violation wording", errors.New("sharing violation"), true},
		{"fs.ErrPermission", &fs.PathError{Op: "open", Err: os.ErrPermission}, true},
		{"missing file is not transient", &fs.PathError{Op: "open", Err: os.ErrNotExist}, false},
		{"read-only filesystem is not transient", errors.New("read-only file system"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientRenameError(tc.err); got != tc.want {
				t.Errorf("isTransientRenameError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestBothStateReadersRetryTransientErrors asserts the retry covers EVERY
// concurrently-written state file, not just the one that happened to fail.
//
// Fixing only the compile-state reader moved the Windows CI failure to the
// batch-state reader — same test, same error, different path. Both now go
// through readStateFileRetrying; this test fails if a reader is added that
// bypasses it.
func TestBothStateReadersRetryTransientErrors(t *testing.T) {
	sharing := errors.New("open x: The process cannot access the file because it is being used by another process.")

	t.Run("readStateFileRetrying retries then succeeds", func(t *testing.T) {
		calls := 0
		data, err := readStateFileRetrying("x", func(string) ([]byte, error) {
			calls++
			if calls <= 2 {
				return nil, sharing
			}
			return []byte(`{"compile_id":"c1"}`), nil
		})
		if err != nil {
			t.Fatalf("expected retry to succeed: %v", err)
		}
		if calls != 3 {
			t.Errorf("expected 3 attempts, got %d", calls)
		}
		if string(data) == "" {
			t.Error("no data returned")
		}
	})

	t.Run("batch-state reader is wired to it", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".sage"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A real file that parses: proves loadBatchCheckpoint reads through the
		// helper without changing its contract.
		if err := os.WriteFile(batchCheckpointPath(dir), []byte(`{"compile_id":"c1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		bcp, err := loadBatchCheckpoint(dir)
		if err != nil || bcp == nil || bcp.CompileID != "c1" {
			t.Fatalf("loadBatchCheckpoint: %v %+v", err, bcp)
		}
	})

	t.Run("missing file still reads as absent", func(t *testing.T) {
		if bcp, err := loadBatchCheckpoint(t.TempDir()); err != nil || bcp != nil {
			t.Errorf("absent checkpoint must be (nil, nil), got %+v %v", bcp, err)
		}
	})
}
