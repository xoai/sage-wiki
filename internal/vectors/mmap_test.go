package vectors

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// setupMmapFixture builds a real workspace DB with seeded doc+chunk vectors
// and returns the storage handle plus the .sage-like dir for index files.
func setupMmapFixture(t *testing.T) (*storage.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

func rebuildBoth(t *testing.T, db *storage.DB, dir string, quant int) {
	t.Helper()
	if _, err := WriteIndexFile(db, IndexTableDocs, filepath.Join(dir, docIndexFile), quant); err != nil {
		t.Fatalf("rebuild docs: %v", err)
	}
	if _, err := WriteIndexFile(db, IndexTableChunks, filepath.Join(dir, chunkIndexFile), quant); err != nil {
		t.Fatalf("rebuild chunks: %v", err)
	}
}

func mmapStore(db *storage.DB, dir string, opts ...Option) *Store {
	base := []Option{WithVectorBackend(backendMmap), WithIndexDir(dir)}
	return NewStore(db, append(base, opts...)...)
}

func TestMmapParity_Synthetic(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)

	// Seeded fixture including EXACT TIES (v1/v4 identical) to pin the
	// insert-stable tie-break order across backends.
	docs := map[string][]float32{
		"v1": {1, 0, 0, 0},
		"v2": {0, 1, 0, 0},
		"v3": {0.9, 0.1, 0, 0},
		"v4": {1, 0, 0, 0},
		"v5": {0, 0, 1, 0},
	}
	for id, v := range docs {
		if err := mem.Upsert(id, v); err != nil {
			t.Fatal(err)
		}
	}
	chunks := [][3]any{
		{"c1", "v1", []float32{1, 0, 0, 0}},
		{"c2", "v1", []float32{0, 1, 0, 0}},
		{"c3", "v2", []float32{1, 0, 0, 0}}, // tie with c1 across docs
		{"c4", "v2", []float32{0.5, 0.5, 0, 0}},
	}
	err := mem.db.WriteTx(func(tx *sql.Tx) error {
		for _, c := range chunks {
			if err := mem.UpsertChunk(tx, c[0].(string), c[1].(string), c[2].([]float32)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mem.InvalidateChunkCache()
	rebuildBoth(t, db, dir, QuantNone)
	mm := mmapStore(db, dir)
	t.Cleanup(func() { _ = mm.Close() })

	q := []float32{1, 0, 0, 0}
	memRes, err := mem.Search(q, 5)
	if err != nil {
		t.Fatal(err)
	}
	mmRes, err := mm.Search(q, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(memRes) != len(mmRes) {
		t.Fatalf("doc result count: memory %d vs mmap %d", len(memRes), len(mmRes))
	}
	for i := range memRes {
		if memRes[i] != mmRes[i] {
			t.Errorf("doc result %d: memory %+v vs mmap %+v", i, memRes[i], mmRes[i])
		}
	}
	// mmap must have served: the memory cache stays unloaded.
	if mm.docCache.isLoaded() {
		t.Error("mmap path must not load the in-memory doc cache")
	}

	// Chunk parity, unfiltered and doc-filtered.
	memChunks, err := mem.SearchChunks(q, 4)
	if err != nil {
		t.Fatal(err)
	}
	mmChunks, err := mm.SearchChunks(q, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(memChunks) != len(mmChunks) {
		t.Fatalf("chunk result count: memory %d vs mmap %d", len(memChunks), len(mmChunks))
	}
	for i := range memChunks {
		if memChunks[i] != mmChunks[i] {
			t.Errorf("chunk result %d: memory %+v vs mmap %+v", i, memChunks[i], mmChunks[i])
		}
	}
	memFilt, err := mem.SearchChunksFiltered(q, []string{"v2"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	mmFilt, err := mm.SearchChunksFiltered(q, []string{"v2"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(memFilt) != len(mmFilt) {
		t.Fatalf("filtered chunk count: memory %d vs mmap %d", len(memFilt), len(mmFilt))
	}
	for i := range memFilt {
		if memFilt[i] != mmFilt[i] {
			t.Errorf("filtered chunk %d: memory %+v vs mmap %+v", i, memFilt[i], mmFilt[i])
		}
	}
	if mm.chunkCache.isLoaded() {
		t.Error("mmap chunk path must not load the in-memory chunk cache")
	}
}

func TestMmap_Missing_FallsBackWithWarn(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0, 0})

	mm := mmapStore(db, dir) // no index files written
	res, err := mm.Search([]float32{1, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != "v1" {
		t.Errorf("fallback search = %+v, want v1", res)
	}
	if !mm.docCache.isLoaded() {
		t.Error("missing index must fall back to the in-memory cache")
	}
}

func TestMmap_CorruptHeader_FallsBack(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0, 0})
	_ = mem.Upsert("v2", []float32{0, 1, 0})

	// Garbage where the doc index should be.
	if err := writeFile(filepath.Join(dir, docIndexFile), []byte("not an index at all")); err != nil {
		t.Fatal(err)
	}
	mm := mmapStore(db, dir)
	res, err := mm.Search([]float32{1, 0, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].ID != "v1" {
		t.Errorf("fallback search = %+v, want [v1 v2]", res)
	}
}

func TestMmap_StaleProbe_FallsBack(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0, 0})
	_ = mem.Upsert("v2", []float32{0, 1, 0})
	rebuildBoth(t, db, dir, QuantNone)

	// A write AFTER the rebuild: probe count mismatches → stale → memory.
	_ = mem.Upsert("v3", []float32{0, 0, 1})
	mm := mmapStore(db, dir)
	res, err := mm.Search([]float32{0, 0, 1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].ID != "v3" {
		t.Errorf("post-write search = %+v, want v3 first of 3", res)
	}
	if !mm.docCache.isLoaded() {
		t.Error("stale snapshot must fall back to the in-memory cache")
	}
}

func TestMmap_MixedDimTable_ProbeTolerates(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("a", []float32{1, 0})
	_ = mem.Upsert("b", []float32{1, 0, 0}) // skipped by writer+loader (dim mismatch)
	_ = mem.Upsert("c", []float32{0, 1})
	rebuildBoth(t, db, dir, QuantNone)

	// The probe counts only dim-matching rows: the snapshot is NOT stale.
	mm := mmapStore(db, dir)
	res, err := mm.Search([]float32{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	memRes, err := mem.Search([]float32{1, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(memRes) {
		t.Fatalf("mmap %d results vs memory %d", len(res), len(memRes))
	}
	for i := range res {
		if res[i] != memRes[i] {
			t.Errorf("result %d: mmap %+v vs memory %+v", i, res[i], memRes[i])
		}
	}
	if mm.docCache.isLoaded() {
		t.Error("mixed-dim table must be served by mmap (probe tolerates skips), not fallback")
	}
}

func TestMmap_WriteInvalidation_FallsBack(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0})
	rebuildBoth(t, db, dir, QuantNone)

	mm := mmapStore(db, dir)
	// Warm the snapshot (serves via mmap).
	if _, err := mm.Search([]float32{1, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if mm.docCache.isLoaded() {
		t.Fatal("precondition: mmap serving, cache cold")
	}
	// Store-owned write marks the snapshot stale immediately.
	if err := mm.Upsert("v2", []float32{0, 1}); err != nil {
		t.Fatal(err)
	}
	res, err := mm.Search([]float32{0, 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].ID != "v2" {
		t.Errorf("post-upsert search = %+v, want v2 first", res)
	}
	if !mm.docCache.isLoaded() {
		t.Error("post-upsert search must use the in-memory path")
	}

	// Same for the chunk table via InvalidateChunkCache.
	mm2 := mmapStore(db, dir)
	mm2.InvalidateChunkCache()
	if mm2.mmChunk == nil || !mm2.mmChunk.stale {
		t.Error("InvalidateChunkCache must mark the chunk snapshot stale")
	}
}

func TestLoaderOrder_RowidExplicit(t *testing.T) {
	db, _ := setupMmapFixture(t)
	s := NewStore(db)
	// Insert in non-alphabetical order: rowid order == insertion order, NOT
	// id-sorted — pins the deterministic loader ordering the mmap writer
	// shares.
	_ = s.Upsert("beta", []float32{1, 0})
	_ = s.Upsert("alpha", []float32{0, 1})
	_ = s.Upsert("gamma", []float32{1, 1})
	if _, err := s.Search([]float32{1, 0}, 3); err != nil {
		t.Fatal(err)
	}
	s.docCache.mu.RLock()
	got := append([]string(nil), s.docCache.ids...)
	s.docCache.mu.RUnlock()
	want := []string{"beta", "alpha", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("cache ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cache ids = %v, want rowid order %v", got, want)
		}
	}
}

func TestANNPlusMmap_ExactScanWins(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0})
	rebuildBoth(t, db, dir, QuantNone)

	s := mmapStore(db, dir, WithANN(true))
	if got := s.IndexKind(); got != "brute-force" {
		t.Errorf("IndexKind = %q, want brute-force (ann ignored with mmap)", got)
	}
	if _, err := s.Search([]float32{1, 0}, 1); err != nil {
		t.Fatal(err)
	}
	if s.docCache.isLoaded() {
		t.Error("mmap exact scan must serve without loading the cache/graph")
	}
}

// F-038 witness: Store.Close unmaps the snapshot (the only release path;
// sqlitestore backend.Close now calls it).
func TestStoreClose_Unmaps(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0})
	rebuildBoth(t, db, dir, QuantNone)

	s := mmapStore(db, dir)
	if _, err := s.Search([]float32{1, 0}, 1); err != nil {
		t.Fatal(err)
	}
	s.mmMu.Lock()
	mapped := s.mmDoc != nil && s.mmDoc.idx != nil
	s.mmMu.Unlock()
	if !mapped {
		t.Fatal("precondition: snapshot mapped after search")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.mmMu.Lock()
	still := s.mmDoc.idx != nil || s.mmDoc.unmap != nil
	s.mmMu.Unlock()
	if still {
		t.Error("Store.Close must unmap the snapshot")
	}
	if err := s.Close(); err != nil {
		t.Error("second Close must be a no-op")
	}
}

// F-047 witness (a): an index rebuilt on an EMPTY table must go stale
// once a later process populates the table — count probe alone passes
// (0 == 0), so this pins the content-drift signal.
func TestMmap_CrossProcessStale_EmptyIndex(t *testing.T) {
	db, dir := setupMmapFixture(t)
	// Rebuild over the empty table.
	rebuildBoth(t, db, dir, QuantNone)

	// A "later process" populates the table (second handle, same file).
	db2, err := storage.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	later := NewStore(db2)
	if err := later.Upsert("v1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}

	mm := mmapStore(db2, dir)
	res, err := mm.Search([]float32{1, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != "v1" {
		t.Errorf("search after cross-process populate = %+v, want v1 (stale empty snapshot must not serve)", res)
	}
	mm.mmMu.Lock()
	stale := mm.mmDoc != nil && mm.mmDoc.stale
	mm.mmMu.Unlock()
	if !stale {
		t.Error("populated-after-empty-rebuild must mark the snapshot stale")
	}
}

// F-047 witness (b): a same-count same-dim re-embed (upsert in place)
// must go stale — the count probe is blind to content changes.
func TestMmap_CrossProcessStale_SameCountReembed(t *testing.T) {
	db, dir := setupMmapFixture(t)
	mem := NewStore(db)
	_ = mem.Upsert("v1", []float32{1, 0})
	_ = mem.Upsert("v2", []float32{0, 1})
	rebuildBoth(t, db, dir, QuantNone)

	// Later process re-embeds v1 to a DIFFERENT vector (count/dim unchanged).
	db2, err := storage.Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	later := NewStore(db2)
	if err := later.Upsert("v1", []float32{0, 1}); err != nil {
		t.Fatal(err)
	}

	mm := mmapStore(db2, dir)
	res, err := mm.Search([]float32{0, 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Correct post-reembed ranking: v1 (now [0,1]) ties v2 — the STALE
	// snapshot would rank v2 strictly first with v1 scoring 0.
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	mm.mmMu.Lock()
	stale := mm.mmDoc != nil && mm.mmDoc.stale
	mm.mmMu.Unlock()
	if !stale {
		t.Error("same-count re-embed must mark the snapshot stale")
	}
	if res[0].Score != res[1].Score {
		t.Errorf("post-reembed scores = %v/%v, want tie (stale snapshot served)", res[0].Score, res[1].Score)
	}
}
