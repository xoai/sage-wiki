package compiler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// seedFailedItem plants a dead-lettered queue row for a source that exists
// on disk (hash deliberately stale so the next scan is a hash change).
func seedFailedItem(t *testing.T, h *workerHarness, path string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	items := NewCompileItemStore(db, config.NowUTC)
	if err := items.Upsert(CompileItem{SourcePath: path, Hash: "stale-hash", FileType: "md", Tier: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := items.Claim(1, "old-worker", time.Hour, 10); err != nil {
		t.Fatal(err)
	}
	if err := items.Release(path, "old-worker", store.ReleaseFailed); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestCompile_CLIGoldenUnchanged(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.writeSource(t, "b.md", "# Beta\n\nBeta content.")

	result, err := Compile(h.dir, CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Summarized != 2 {
		t.Errorf("Summarized = %d, want 2", result.Summarized)
	}
	// Queue settled: both items done, no leases left behind.
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	items := NewCompileItemStore(db, config.NowUTC)
	for _, p := range []string{"raw/a.md", "raw/b.md"} {
		got, _ := items.GetByPath(p)
		if got == nil {
			t.Fatalf("%s missing from queue", p)
		}
		if got.Status != "done" {
			t.Errorf("%s status = %q, want done (CLI settles its claims)", p, got.Status)
		}
		if got.LeaseOwner != "" {
			t.Errorf("%s lease leaked: %q", p, got.LeaseOwner)
		}
	}
	// Manifest + summaries on disk, as before P2-3.
	matches, _ := filepath.Glob(filepath.Join(h.dir, "wiki", "summaries", "*"))
	if len(matches) != 2 {
		t.Errorf("summaries = %d, want 2", len(matches))
	}
}

func TestCompile_FreshResetsFailed(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	if _, err := Compile(h.dir, CompileOpts{Fresh: true}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	got, _ := NewCompileItemStore(db, config.NowUTC).GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done (--fresh reset the dead letter)", got.Status)
	}
}

func TestCompile_HashChangeRevivesFailed(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content — edited after failing.")
	seedFailedItem(t, h, "raw/a.md")

	// No --fresh: the hash change alone must revive the item.
	if _, err := Compile(h.dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	got, _ := NewCompileItemStore(db, config.NowUTC).GetByPath("raw/a.md")
	if got.Status != "done" {
		t.Errorf("status = %q, want done (hash change revived the dead letter)", got.Status)
	}
}

func TestCompile_FailedItemsSkipped(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	// Compile with nothing new: the dead-lettered item must NOT be retried
	// (the hash matches after the seed's hash is refreshed by a first scan).
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	items := NewCompileItemStore(db, config.NowUTC)
	// Simulate the first scan having already adopted the real hash while
	// keeping the dead letter (same-hash upsert preserves queue state).
	if _, err := Compile(h.dir, CompileOpts{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	got, _ := items.GetByPath("raw/a.md")
	if got.Status != "failed" {
		t.Errorf("status = %q after dry run, want failed (no side effects)", got.Status)
	}
	db.Close()
}

func TestCompile_DryRunNoSideEffects(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	seedFailedItem(t, h, "raw/a.md")

	before, _ := h.items.GetByPath("raw/a.md")
	if _, err := Compile(h.dir, CompileOpts{DryRun: true, Fresh: true}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	after, _ := h.items.GetByPath("raw/a.md")
	if *before != *after {
		t.Errorf("dry run mutated queue state: before %+v after %+v", before, after)
	}
}

// testBackend is the minimal store.Backend for Compile- and worker-cycle
// tests, built like setupStores' legacy sqlite path but over ONE
// *storage.DB handle (R3: worker tests must mirror serve-mode ownership,
// where Items and the pass stores share a backend). OutputIndex and
// Learnings stay nil and BeginWrite stays an explicit unsupported stub —
// neither the Compile path nor a worker cycle reaches those surfaces.
type testBackend struct {
	db      *storage.DB
	items   store.CompileItemStore
	entries store.EntryStore
	chunks  store.ChunkStore
	vecs    store.VectorStore
	ont     store.OntologyStore
	trust   store.TrustStore
}

func newTestBackend(db *storage.DB) *testBackend {
	merged := ontology.MergedRelations(nil)
	mergedTypes := ontology.MergedEntityTypes(nil)
	return &testBackend{
		db:      db,
		items:   NewCompileItemStore(db, config.NowUTC),
		entries: memory.NewStore(db),
		chunks:  memory.NewChunkStore(db),
		vecs:    vectors.NewStore(db),
		ont:     ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes)),
		trust:   trust.NewStore(db),
	}
}

func (b *testBackend) Entries() store.EntryStore         { return b.entries }
func (b *testBackend) Chunks() store.ChunkStore          { return b.chunks }
func (b *testBackend) Vectors() store.VectorStore        { return b.vecs }
func (b *testBackend) Ontology() store.OntologyStore     { return b.ont }
func (b *testBackend) Communities() store.CommunityStore { return b.ont.(store.CommunityStore) }
func (b *testBackend) Trust() store.TrustStore           { return b.trust }
func (b *testBackend) CompileItems() store.CompileItemStore {
	return b.items
}
func (b *testBackend) OutputIndex() store.OutputIndexStore { return nil }
func (b *testBackend) Learnings() store.LearningStore      { return nil }
func (b *testBackend) WriteTx(fn func(tx *sql.Tx) error) error {
	return b.db.WriteTx(fn)
}
func (b *testBackend) BeginWrite() (*store.Tx, error) { return nil, errors.New("unsupported") }
func (b *testBackend) ReadDB() *sql.DB                { return b.db.ReadDB() }
func (b *testBackend) WriteDB() *sql.DB               { return b.db.WriteDB() }
func (b *testBackend) Health(context.Context) error   { return nil }
func (b *testBackend) SchemaReady() bool              { return true }
func (b *testBackend) Location() string               { return "test" }
func (b *testBackend) Close() error                   { return nil }

// erroringClaimStore wraps a real queue store and fails every Claim —
// proving a broken queue store surfaces on the CLI path instead of
// silently compiling nothing (Gate-3 review, MAJOR).
type erroringClaimStore struct {
	store.CompileItemStore
	err error
}

func (e *erroringClaimStore) Claim(tier int, owner string, ttl time.Duration, limit int) ([]CompileItem, error) {
	return nil, e.err
}

type erroringBackend struct {
	store.Backend
	items store.CompileItemStore
}

func (b *erroringBackend) CompileItems() store.CompileItemStore { return b.items }

func TestCompile_ClaimErrorSurfaces(t *testing.T) {
	h := newWorkerHarness(t, 1, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")

	// A real sqlite backend (built like setupStores' legacy path) whose
	// queue store fails every Claim.
	sdb, err := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	tb := newTestBackend(sdb)
	backend := &erroringBackend{
		Backend: tb,
		items:   &erroringClaimStore{CompileItemStore: tb.items, err: errors.New("queue store down")},
	}
	result, err := Compile(h.dir, CompileOpts{Backend: backend})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Errors == 0 {
		t.Error("claim failures did not surface in CompileResult.Errors")
	}
}

// Regression (independent review, CRITICAL): an item claimed at tier 3 but
// absent from the current diff — e.g. an auto-promoted source whose file
// is unchanged — must NOT be treated as failed. Pre-fix, every unrelated
// compile burned one attempt until the healthy item dead-lettered.
func TestCompile_PromotedItemNotBurned(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	h.writeSource(t, "b.md", "# Beta\n\nBeta content.")

	// Compile both fully: manifest saved, items done.
	if _, err := Compile(h.dir, CompileOpts{}); err != nil {
		t.Fatalf("compile 1: %v", err)
	}

	// Simulate auto-promotion: a.md owes the tier-3 passes again but its
	// file is unchanged (manifest hash matches → no diff for a).
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	if _, err := db.WriteDB().Exec(`UPDATE compile_items
		SET pass_summarized = 0, pass_extracted = 0, pass_written = 0,
		    status = 'pending', attempts = 0
		WHERE source_path = 'raw/a.md'`); err != nil {
		t.Fatal(err)
	}

	// Modify only b.md → the next compile claims a.md at tier 3 (passes
	// owed) but never processes it (not in the diff).
	h.writeSource(t, "b.md", "# Beta v2\n\nBeta content, edited.")
	if _, err := Compile(h.dir, CompileOpts{}); err != nil {
		t.Fatalf("compile 2: %v", err)
	}

	got, _ := NewCompileItemStore(db, config.NowUTC).GetByPath("raw/a.md")
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 — unprocessed claimed item must not burn budget", got.Attempts)
	}
	if got.Status == "failed" {
		t.Error("healthy promoted item dead-lettered without ever being processed")
	}
	if got.LeaseOwner != "" {
		t.Errorf("lease leaked on unprocessed claimed item: %q", got.LeaseOwner)
	}
}

// Regression (independent review, MAJOR): --fresh must revive a dead
// letter even when nothing changed (empty diff early-return path).
func TestCompile_FreshRevivesOnEmptyDiff(t *testing.T) {
	h := newWorkerHarness(t, 3, http.StatusOK)
	h.writeSource(t, "a.md", "# Alpha\n\nAlpha content.")
	if _, err := Compile(h.dir, CompileOpts{}); err != nil {
		t.Fatalf("compile 1: %v", err)
	}

	// Dead-letter the item by hand (file unchanged → next diff is empty).
	db, _ := storage.Open(filepath.Join(h.dir, ".sage", "wiki.db"))
	defer db.Close()
	if _, err := db.WriteDB().Exec(`UPDATE compile_items
		SET status = 'failed', attempts = 5 WHERE source_path = 'raw/a.md'`); err != nil {
		t.Fatal(err)
	}

	if _, err := Compile(h.dir, CompileOpts{Fresh: true}); err != nil {
		t.Fatalf("compile --fresh: %v", err)
	}
	got, _ := NewCompileItemStore(db, config.NowUTC).GetByPath("raw/a.md")
	if got.Status != "pending" || got.Attempts != 0 {
		t.Errorf("status = %q attempts = %d, want pending/0 — --fresh must revive on empty diff", got.Status, got.Attempts)
	}
}
