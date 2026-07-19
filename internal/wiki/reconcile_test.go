package wiki

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// countingEmbedder records how many Embed calls happen, so a test can assert
// that a consistent output is NOT re-embedded.
type countingEmbedder struct{ n int32 }

func (e *countingEmbedder) Embed(string) ([]float32, error) { atomic.AddInt32(&e.n, 1); return []float32{0.1, 0.2}, nil }
func (e *countingEmbedder) Dimensions() int                 { return 2 }
func (e *countingEmbedder) Name() string                    { return "counting" }
func (e *countingEmbedder) calls() int                      { return int(atomic.LoadInt32(&e.n)) }

type reconcileEnv struct {
	dir string
	cfg *config.Config
	db  *storage.DB
	mem *memory.Store
	ont *ontology.Store
	vec *vectors.Store
	oi  *storage.OutputIndex
}

// addChunkVector marks an output (by docID) as having a chunk vector, so it looks
// fully indexed to the reconciler's vector-completeness check.
func (e *reconcileEnv) addChunkVector(t *testing.T, docID string) {
	t.Helper()
	err := e.db.WriteTx(func(tx *sql.Tx) error {
		return e.vec.UpsertChunk(tx, docID+":c0", docID, []float32{0.1, 0.2})
	})
	if err != nil {
		t.Fatalf("seed chunk vector for %s: %v", docID, err)
	}
}

func setupReconcile(t *testing.T) *reconcileEnv {
	t.Helper()
	dir := t.TempDir()
	if err := InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	return &reconcileEnv{
		dir: dir, cfg: cfg, db: db,
		mem: memory.NewStore(db),
		ont: ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes)),
		vec: vectors.NewStore(db),
		oi:  storage.NewOutputIndex(db),
	}
}

// writeConceptFile writes an article output file and returns its project-relative path.
func (e *reconcileEnv) writeConceptFile(t *testing.T, name, content string) string {
	t.Helper()
	rel := filepath.Join(e.cfg.Output, "concepts", name+".md")
	abs := filepath.Join(e.dir, rel)
	os.MkdirAll(filepath.Dir(abs), 0755)
	if err := os.WriteFile(abs, []byte(content), 0644); err != nil {
		t.Fatalf("write concept %s: %v", name, err)
	}
	return rel
}

func (e *reconcileEnv) saveManifest(t *testing.T, m *manifest.Manifest) {
	t.Helper()
	if err := m.Save(filepath.Join(e.dir, ".manifest.json")); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
}

func TestReconcile_FileNoDB_Indexes(t *testing.T) {
	e := setupReconcile(t)
	rel := e.writeConceptFile(t, "alpha", "# Alpha\n\nContent about alpha.")

	m := manifest.New()
	m.AddConcept("alpha", rel, []string{"raw/a.md"})
	e.saveManifest(t, m)
	// alpha is on disk + in the manifest but NOT in the DB (crash between write and index).

	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, &countingEmbedder{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1", res.Reindexed)
	}
	if got, _ := e.mem.Get("concept:alpha"); got == nil {
		t.Error("alpha not indexed into FTS")
	}
	if ent, _ := e.ont.GetEntity("alpha"); ent == nil {
		t.Error("alpha ontology entity not created")
	}
	if _, ok, _ := e.oi.Get(rel); !ok {
		t.Error("alpha output_index hash not recorded")
	}
}

func TestReconcile_DBNoFile_Drops(t *testing.T) {
	e := setupReconcile(t)
	rel := filepath.Join(e.cfg.Output, "concepts", "beta.md") // NOT written to disk

	// Indexed in DB but the file vanished.
	e.mem.Add(memory.Entry{ID: "concept:beta", Content: "stale", ArticlePath: rel})
	e.ont.AddEntity(ontology.Entity{ID: "beta", Type: ontology.TypeConcept, Name: "beta", ArticlePath: rel})
	e.oi.Set(rel, "oldhash")

	m := manifest.New()
	m.AddConcept("beta", rel, []string{"raw/b.md"})
	e.saveManifest(t, m)

	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, &countingEmbedder{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", res.Dropped)
	}
	if got, _ := e.mem.Get("concept:beta"); got != nil {
		t.Error("beta FTS entry not dropped")
	}
	if _, ok, _ := e.oi.Get(rel); ok {
		t.Error("beta output_index row not dropped")
	}
}

func TestReconcile_ChangedOutput_Reindexes(t *testing.T) {
	e := setupReconcile(t)
	rel := e.writeConceptFile(t, "gamma", "# Gamma\n\nNEW content.")

	// Indexed with the OLD content + a stale output hash.
	e.mem.Add(memory.Entry{ID: "concept:gamma", Content: "OLD content", ArticlePath: rel})
	e.ont.AddEntity(ontology.Entity{ID: "gamma", Type: ontology.TypeConcept, Name: "gamma", ArticlePath: rel})
	e.oi.Set(rel, storage.HashBytes([]byte("# Gamma\n\nOLD content.")))

	m := manifest.New()
	m.AddConcept("gamma", rel, []string{"raw/g.md"})
	e.saveManifest(t, m)

	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, &countingEmbedder{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1 (changed output)", res.Reindexed)
	}
	got, _ := e.mem.Get("concept:gamma")
	if got == nil || got.Content != "# Gamma\n\nNEW content." {
		t.Errorf("gamma FTS not updated to new content: %+v", got)
	}
	want := storage.HashBytes([]byte("# Gamma\n\nNEW content."))
	if h, _, _ := e.oi.Get(rel); h != want {
		t.Errorf("gamma output hash = %q, want %q", h, want)
	}
}

func TestReconcile_Consistent_NoReembed(t *testing.T) {
	e := setupReconcile(t)
	content := "# Delta\n\nStable content."
	rel := e.writeConceptFile(t, "delta", content)

	// Fully indexed (FTS + ontology + a chunk vector); hash NOT yet recorded
	// (pre-upgrade / fresh-compile state).
	e.mem.Add(memory.Entry{ID: "concept:delta", Content: content, ArticlePath: rel})
	e.ont.AddEntity(ontology.Entity{ID: "delta", Type: ontology.TypeConcept, Name: "delta", ArticlePath: rel})
	e.addChunkVector(t, "concept:delta")

	m := manifest.New()
	m.AddConcept("delta", rel, []string{"raw/d.md"})
	e.saveManifest(t, m)

	emb := &countingEmbedder{}
	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, emb)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Reindexed != 0 {
		t.Errorf("Reindexed = %d, want 0 (already indexed — only record hash)", res.Reindexed)
	}
	if emb.calls() != 0 {
		t.Errorf("embedder called %d times — an already-indexed output must not be re-embedded", emb.calls())
	}
	if _, ok, _ := e.oi.Get(rel); !ok {
		t.Error("hash should be recorded for the already-indexed output")
	}
}

// TestStripFrontmatter pins the summary body extraction against the exact
// frontmatter format the compiler writes (`---\n...\n---\n\n` + body), so the #3
// FTS-content comparison lines up with what the compiler indexed.
func TestStripFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"compiler summary format", "---\nsource: raw/x.md\ncompiled_at: 2026-07-19\n---\n\n# Attention\n\nBody text.", "# Attention\n\nBody text."},
		{"no frontmatter", "# Plain\n\nNo frontmatter here.", "# Plain\n\nNo frontmatter here."},
		{"body contains a rule", "---\nsource: x\n---\n\nAbove.\n---\nBelow.", "Above.\n---\nBelow."},
		{"body legitimately starts with newline", "---\nsource: x\n---\n\n\nleading blank", "\nleading blank"},
		{"malformed (no close) left as-is", "---\nsource: x\nno close", "---\nsource: x\nno close"},
	}
	for _, tt := range tests {
		if got := stripFrontmatter(tt.in); got != tt.want {
			t.Errorf("%s: stripFrontmatter(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// TestReconcile_CompileUpdatedFTS_NoReembed is the #3 regression: a live compile
// re-indexed an output (FTS + vectors reflect the new content) but did not update
// output_index, so the recorded hash lags. The reconciler must NOT re-embed —
// the DB already reflects the file — else a full recompile re-embeds the whole
// vault. It only refreshes the stale output_index hash.
func TestReconcile_CompileUpdatedFTS_NoReembed(t *testing.T) {
	e := setupReconcile(t)
	content := "# Theta\n\nRecompiled content."
	rel := e.writeConceptFile(t, "theta", content)

	// FTS + vectors already reflect the CURRENT file (compile did this)...
	e.mem.Add(memory.Entry{ID: "concept:theta", Content: content, ArticlePath: rel})
	e.ont.AddEntity(ontology.Entity{ID: "theta", Type: ontology.TypeConcept, Name: "theta", ArticlePath: rel})
	e.addChunkVector(t, "concept:theta")
	// ...but output_index still has the PRE-recompile hash.
	e.oi.Set(rel, storage.HashBytes([]byte("# Theta\n\nOLD content.")))

	m := manifest.New()
	m.AddConcept("theta", rel, []string{"raw/t.md"})
	e.saveManifest(t, m)

	emb := &countingEmbedder{}
	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, emb)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.Reindexed != 0 {
		t.Errorf("Reindexed = %d, want 0 (FTS already current — must not re-embed)", res.Reindexed)
	}
	if emb.calls() != 0 {
		t.Errorf("embedder called %d times — a compile-current output must not be re-embedded", emb.calls())
	}
	// The stale output_index hash is refreshed to the current file hash.
	if h, _, _ := e.oi.Get(rel); h != storage.HashBytes([]byte(content)) {
		t.Errorf("output_index hash not refreshed: %q", h)
	}
}

// TestReconcile_OfflineThenOnline_FillsVectors is the #2 regression: an output
// indexed while the embedder was offline has no vectors; a later reconcile with a
// working embedder must fill them in (not skip forever because a hash was
// recorded).
func TestReconcile_OfflineThenOnline_FillsVectors(t *testing.T) {
	e := setupReconcile(t)
	rel := e.writeConceptFile(t, "iota", "# Iota\n\nOffline-indexed article.")
	m := manifest.New()
	m.AddConcept("iota", rel, []string{"raw/i.md"})
	e.saveManifest(t, m)

	// First reconcile OFFLINE: indexes FTS/ontology, defers vectors, records hash.
	off, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, nil)
	if err != nil {
		t.Fatalf("offline reconcile: %v", err)
	}
	if off.VectorsDeferred == 0 {
		t.Fatal("expected vectors deferred offline")
	}
	if has, _ := e.vec.HasChunkVectors("concept:iota"); has {
		t.Fatal("did not expect chunk vectors after offline reconcile")
	}

	// Second reconcile ONLINE: must re-index to fill the missing vectors.
	emb := &countingEmbedder{}
	on, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, emb)
	if err != nil {
		t.Fatalf("online reconcile: %v", err)
	}
	if on.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1 (fill deferred vectors)", on.Reindexed)
	}
	if emb.calls() == 0 {
		t.Error("embedder not called — deferred vectors were never filled")
	}
	if has, _ := e.vec.HasChunkVectors("concept:iota"); !has {
		t.Error("chunk vectors still missing after online reconcile")
	}
}

func TestReconcile_OfflineEmbedder_DefersVectors(t *testing.T) {
	e := setupReconcile(t)
	rel := e.writeConceptFile(t, "epsilon", "# Epsilon\n\nOffline reconcile.")

	m := manifest.New()
	m.AddConcept("epsilon", rel, []string{"raw/e.md"})
	e.saveManifest(t, m)

	// nil embedder → offline.
	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, nil)
	if err != nil {
		t.Fatalf("Reconcile offline: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1", res.Reindexed)
	}
	if res.VectorsDeferred == 0 {
		t.Error("expected VectorsDeferred > 0 when embedder is offline")
	}
	// FTS + ontology still reconciled offline.
	if got, _ := e.mem.Get("concept:epsilon"); got == nil {
		t.Error("epsilon not indexed into FTS offline")
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	e := setupReconcile(t)
	rel := e.writeConceptFile(t, "zeta", "# Zeta\n\nContent.")
	m := manifest.New()
	m.AddConcept("zeta", rel, []string{"raw/z.md"})
	e.saveManifest(t, m)

	if _, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, &countingEmbedder{}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	emb := &countingEmbedder{}
	res, err := Reconcile(context.Background(), e.dir, e.cfg, e.db, emb)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if res.Reindexed != 0 || res.Dropped != 0 {
		t.Errorf("second reconcile not a no-op: reindexed=%d dropped=%d", res.Reindexed, res.Dropped)
	}
	if emb.calls() != 0 {
		t.Errorf("second reconcile re-embedded %d times, want 0", emb.calls())
	}
}
