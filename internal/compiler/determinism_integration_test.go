package compiler

import (
	"fmt"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// volatileStatePrefixes enumerates the operational-state files excluded
// from byte-parity (SPEC-04 D7): wall-clock/goroutine-timing state that is
// not an artifact. docs/determinism.md mirrors this exact list — if you
// change one, change the other.
var volatileStatePrefixes = []string{
	".sage/usage.jsonl",      // usage ledger (goroutine append order, wall clock)
	".sage/jobs.jsonl",       // serve-mode job log (RFC3339Nano + UnixNano ids)
	".sage/lintlog/",         // linter reports (wall-clock filenames + durations)
	".sage/engine.lock",      // flock token (pid + time)
	".sage/batch-state.json", // batch checkpoint (provider batch ids)
	".sage/compile-state.json",
	".sage/wiki.db-wal", // SQLite WAL (checkpoints vary)
	".sage/wiki.db-shm", // SQLite shared memory
}

func isVolatileState(rel string) bool {
	for _, p := range volatileStatePrefixes {
		if rel == p || strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// TestDoubleCompile_ByteParity is AC-1: two from-scratch compiles of the
// same corpus produce byte-identical workspaces modulo the documented
// volatile operational state. wiki/, .manifest.json, wiki.db, and vector
// index files MUST be byte-identical.
func TestDoubleCompile_ByteParity(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	dirA := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})
	dirB := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	drift := spikeDiffTrees(t, dirA, dirB)
	var real []string
	for _, d := range drift {
		rel := strings.Fields(d)[0]
		if !isVolatileState(rel) {
			real = append(real, d)
		}
	}
	for _, d := range drift {
		t.Logf("drift: %s%s", d, map[bool]string{true: " (volatile, excluded)", false: ""}[isVolatileState(strings.Fields(d)[0])])
	}
	if len(real) > 0 {
		t.Errorf("AC-1: %d non-volatile paths differ between identical compiles: %v", len(real), real)
	}
}

// TestDoubleCompile_ByteParityUnderConcurrency is AC-9: byte-parity holds
// with max_parallel > 1 AND scrambled goroutine completion order — input-
// order application proven, not max_parallel:1 pinning.
func TestDoubleCompile_ByteParityUnderConcurrency(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	// ONE server (identical base_url → identical config.yaml input); the
	// delay map SWAPS between runs to invert completion order.
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()

	// Scramble A: bbb's write is slow (completes last despite being second).
	stub.mu.Lock()
	stub.writeDelays = map[string]time.Duration{"concept-bbb": 500 * time.Millisecond}
	stub.embedDelays = map[string]time.Duration{"Deferred Doc 2": 500 * time.Millisecond}
	stub.mu.Unlock()
	dirA := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	// Scramble B: the OPPOSITE ends are slow (completion order inverted).
	stub.mu.Lock()
	stub.writeDelays = map[string]time.Duration{"concept-aaa": 500 * time.Millisecond}
	stub.embedDelays = map[string]time.Duration{"Deferred Doc 1": 500 * time.Millisecond}
	stub.mu.Unlock()
	dirB := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	drift := spikeDiffTrees(t, dirA, dirB)
	var real []string
	for _, d := range drift {
		rel := strings.Fields(d)[0]
		if !isVolatileState(rel) {
			real = append(real, d)
		}
	}
	if len(real) > 0 {
		t.Errorf("AC-9: %d non-volatile paths differ under scrambled completion order: %v", len(real), real)
		if keep := os.Getenv("SAGE_AC9_KEEP"); keep != "" {
			for i, d := range []string{dirA, dirB} {
				dst := filepath.Join(keep, fmt.Sprintf("cc%d", i))
				os.MkdirAll(filepath.Join(dst, ".sage"), 0755)
				b, _ := os.ReadFile(filepath.Join(d, ".sage", "wiki.db"))
				os.WriteFile(filepath.Join(dst, ".sage", "wiki.db"), b, 0644)
			}
			t.Logf("failing DBs preserved under %s", keep)
		}
	}
}

// TestCompile_ExplainMatchesSpecComposition is AC-5's CLI-adjacent check at
// the compiler layer: the explanation's composition matches spec §The
// compile key field-for-field on a real compiled workspace.
func TestCompile_ExplainMatchesSpecComposition(t *testing.T) {
	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := newTestServer(stub)
	defer srv.Close()
	dir := compileDeferredCorpusOpts(t, srv.URL, CompileOpts{})

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sdb, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	items := NewCompileItemStore(sdb, config.NowUTC)

	ex, err := ExplainCompileKey(dir, "raw/doc1.md", cfg, nil, items)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Verdict != "skip: unchanged" {
		t.Errorf("verdict = %q, want skip: unchanged", ex.Verdict)
	}
	if ex.Key != ex.StoredKey {
		t.Errorf("computed key %q != stored %q", ex.Key, ex.StoredKey)
	}
	if ex.CurrentParts.Templates == "" || !strings.Contains(ex.CurrentParts.Templates, "@1.0.0:") {
		t.Errorf("templates component malformed: %q", ex.CurrentParts.Templates)
	}
	if !strings.HasPrefix(ex.CurrentParts.Source, "sha256:") {
		t.Errorf("source component malformed: %q", ex.CurrentParts.Source)
	}
}

// TestBatchResume_OrderIndependentByteParity (SPEC-04 AC-1 for the batch
// path): providers return batch results in arbitrary order. Two compiles
// whose retrieved results arrive in OPPOSITE orders must still produce
// byte-identical artifacts. Without a deterministic sort before the apply
// loop, memStore/vecStore insertion (and thus wiki.db) follows provider-
// return order and the two trees drift.
func TestBatchResume_OrderIndependentByteParity(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	defer fake.Close()

	idA, idB := batchIDForPath("raw/a.md"), batchIDForPath("raw/b.md")
	if idA == idB {
		t.Fatal("test fixture: batch ids collide")
	}

	run := func(t *testing.T, resultOrder []string) string {
		dir := writeBatchProject(t, fake.URL, "", "raw/a.md", "raw/b.md")
		if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
			CompileID: "c1",
			Batch: &BatchState{
				BatchID:  "batch_test_1",
				Provider: "openai",
				Pass:     "summarize",
				PathByID: map[string]string{idA: "raw/a.md", idB: "raw/b.md"},
			},
			Pending: []string{"raw/a.md", "raw/b.md"},
		}); err != nil {
			t.Fatal(err)
		}
		fake.setResults(resultOrder)
		if _, err := Compile(dir, CompileOpts{}); err != nil {
			t.Fatalf("batch compile: %v", err)
		}
		return dir
	}

	forward := []string{idA, idB}
	reversed := []string{idB, idA}

	dirA := run(t, forward)
	dirB := run(t, reversed)

	// Compare the wiki/ output directory (summaries, articles) — the
	// user-facing artifacts. Raw wiki.db BYTES are sensitive to SQLite
	// page-layout timing under -race; instead, query the FTS rowid→id
	// mapping directly (the actual thing the batch sort protects: FTS
	// insertion order).
	drift := spikeDiffTrees(t, dirA, dirB)
	var realDrift []string
	for _, d := range drift {
		rel := strings.Fields(d)[0]
		// Exclude wiki.db (page-layout timing under -race) and the known
		// volatile files — the FTS query below covers wiki.db's logical
		// content.
		if isVolatileState(rel) || strings.HasSuffix(rel, "wiki.db") {
			continue
		}
		realDrift = append(realDrift, d)
	}
	if len(realDrift) > 0 {
		t.Errorf("SPEC-04 batch path: non-volatile drift: %v", realDrift)
	}

	// FTS rowid→id mapping: the batch sort's determinism guarantee.
	// Without the sort, the two runs produce different rowid assignments
	// (the provider-return order leaks into insertion order).
	ftsOrder := func(dir string) []string {
		sdb, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer sdb.Close()
		rows, err := sdb.ReadDB().Query("SELECT id FROM entries ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	aOrder := ftsOrder(dirA)
	bOrder := ftsOrder(dirB)
	if len(aOrder) != len(bOrder) {
		t.Fatalf("FTS entry count: A=%d B=%d", len(aOrder), len(bOrder))
	}
	for i := range aOrder {
		if aOrder[i] != bOrder[i] {
			t.Errorf("SPEC-04 batch path: FTS rowid %d maps to %q in A but %q in B — provider-return order leaked into FTS insertion order", i, aOrder[i], bOrder[i])
			break
		}
	}
}
