package vectors

import (
	"database/sql"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
)

// seededFixture builds a deterministic DB with doc-level and chunk vectors.
// Vectors are pseudo-random unit-ish directions; no exact score ties.
// One doc entry is a ZERO vector (zero-norm guard, i1).
func seededFixture(t *testing.T, docs, chunksPerDoc int) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := NewStore(db)

	rng := uint32(42)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	vec := func(dim int) []float32 {
		v := make([]float32, dim)
		for i := range v {
			v[i] = next()
		}
		return v
	}

	const dim = 8
	for d := 0; d < docs; d++ {
		var v []float32
		if d == 0 {
			v = make([]float32, dim) // zero vector, zero-norm guard
		} else {
			v = vec(dim)
		}
		if err := s.Upsert(docID(d), v); err != nil {
			t.Fatal(err)
		}
		for c := 0; c < chunksPerDoc; c++ {
			cid := chunkID(d, c)
			if err := db.WriteTx(func(tx *sql.Tx) error {
				return s.UpsertChunk(tx, cid, docID(d), vec(dim))
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, func() { db.Close() }
}

func docID(d int) string      { return "doc-" + itoa(d) }
func chunkID(d, c int) string { return docID(d) + ":chunk-" + itoa(c) }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// bruteForceDoc computes the reference top-K with the ORIGINAL
// CosineSimilarity semantics over the fixture read straight from the DB.
func bruteForceDoc(t *testing.T, s *Store, query []float32, limit int) []VectorResult {
	t.Helper()
	// Read all entries through the public (uncached-after-T1) SQL path by
	// constructing a second Store on the same DB and forcing... simplest:
	// compute from the same rows via Get-by-iteration is unavailable, so
	// re-query via a fresh Store sharing the DB file is overkill — instead
	// mirror the fixture deterministically is fragile; the honest reference
	// is CosineSimilarity over ALL rows via a direct SQL read.
	rows, err := s.db.ReadDB().Query("SELECT id, embedding FROM vec_entries")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var results []VectorResult
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			t.Fatal(err)
		}
		vec := decodeFloat32s(blob)
		if len(vec) != len(query) {
			continue
		}
		results = insertSorted(results, VectorResult{ID: id, Score: CosineSimilarity(query, vec)}, limit)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for i := range results {
		results[i].Rank = i + 1
	}
	return results
}

func TestCache_Parity_DocLevel(t *testing.T) {
	s, cleanup := seededFixture(t, 50, 0)
	defer cleanup()

	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}
	want := bruteForceDoc(t, s, query, 10)

	got, err := s.Search(query, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertParity(t, want, got)

	// Second search must hit the cache (load counter stays 1) with same result.
	if s.docCache.loadCount() != 1 {
		t.Fatalf("loadCount = %d, want 1", s.docCache.loadCount())
	}
	got2, _ := s.Search(query, 10)
	assertParity(t, want, got2)
	if s.docCache.loadCount() != 1 {
		t.Errorf("second search reloaded: loadCount = %d", s.docCache.loadCount())
	}
}

func assertParity(t *testing.T, want, got []VectorResult) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("len: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if want[i].ID != got[i].ID {
			t.Fatalf("rank %d: want %s (%.6f), got %s (%.6f)", i, want[i].ID, want[i].Score, got[i].ID, got[i].Score)
		}
		if math.Abs(want[i].Score-got[i].Score) > 1e-6 {
			t.Errorf("score %s: want %.8f, got %.8f", want[i].ID, want[i].Score, got[i].Score)
		}
	}
}

func TestCache_ZeroNormRow(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 0)
	defer cleanup()
	// doc-0 is the zero vector. A nonzero query: cosine = 0, cache dot = 0 —
	// no NaN anywhere, doc-0 simply ranks last (or out of top-K).
	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}
	got, err := s.Search(query, 50)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range got {
		if math.IsNaN(r.Score) {
			t.Fatalf("NaN score for %s", r.ID)
		}
		if r.ID == "doc-0" && r.Score != 0 {
			t.Errorf("zero vector scored %f, want 0", r.Score)
		}
	}
}

func TestCache_PatchOnUnloaded(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 0)
	defer cleanup()

	// Patch before ANY search: must be a no-op and must NOT set loaded —
	// a materialized partial cache would serve incomplete results forever.
	s.Upsert("doc-new", []float32{1, 0, 0, 0, 0, 0, 0, 0})
	if s.docCache.isLoaded() {
		t.Fatal("patch on unloaded cache set loaded=true")
	}
	got, err := s.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "doc-new" {
		t.Errorf("fresh load after unloaded patch missed the upsert: %+v", got)
	}
}

func TestCache_OwnedInvalidation(t *testing.T) {
	s, cleanup := seededFixture(t, 10, 0)
	defer cleanup()

	query := []float32{1, 0, 0, 0, 0, 0, 0, 0}
	s.Search(query, 3)
	if s.docCache.loadCount() != 1 {
		t.Fatalf("loadCount = %d", s.docCache.loadCount())
	}

	// Upsert a new top-1 — incremental patch, NO reload.
	s.Upsert("champion", []float32{1, 0, 0, 0, 0, 0, 0, 0})
	got, _ := s.Search(query, 3)
	if len(got) == 0 || got[0].ID != "champion" {
		t.Errorf("upsert not reflected: %+v", got)
	}
	if s.docCache.loadCount() != 1 {
		t.Errorf("upsert caused reload: loadCount = %d", s.docCache.loadCount())
	}

	// Delete the top-1 — patch again, still no reload.
	s.Delete("champion")
	got, _ = s.Search(query, 3)
	for _, r := range got {
		if r.ID == "champion" {
			t.Error("delete not reflected")
		}
	}
	if s.docCache.loadCount() != 1 {
		t.Errorf("delete caused reload: loadCount = %d", s.docCache.loadCount())
	}
}

func TestCache_SingleFlight(t *testing.T) {
	s, cleanup := seededFixture(t, 20, 0)
	defer cleanup()

	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Search(query, 5)
		}()
	}
	wg.Wait()
	if got := s.docCache.loadCount(); got != 1 {
		t.Errorf("concurrent first searches loaded %d times, want exactly 1", got)
	}
}

func TestCache_MixedWorkloadRace(t *testing.T) {
	s, cleanup := seededFixture(t, 30, 2)
	defer cleanup()

	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				s.Search(query, 5)
				s.SearchChunks(query, 5)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s.Upsert(docID(100+n*10+j), []float32{1, 0.1, 0, 0, 0, 0, 0, 0})
				s.Delete(docID(100 + n*10 + j))
				s.InvalidateChunkCache()
			}
		}(i)
	}
	wg.Wait()
	// Postcondition: cache coherent with DB after the dust settles.
	want := bruteForceDoc(t, s, query, 5)
	got, _ := s.Search(query, 5)
	assertParity(t, want, got)
}

func TestCache_QueryNotMutated(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 0)
	defer cleanup()
	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}
	orig := make([]float32, len(query))
	copy(orig, query)
	if _, err := s.Search(query, 3); err != nil {
		t.Fatal(err)
	}
	for i := range query {
		if query[i] != orig[i] {
			t.Fatalf("query slice mutated at %d: %v -> %v", i, orig, query)
		}
	}
}

func TestCache_ChunkParityAndFiltered(t *testing.T) {
	s, cleanup := seededFixture(t, 10, 5)
	defer cleanup()

	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}

	// Unfiltered parity vs brute-force SQL reference.
	rows, err := s.db.ReadDB().Query("SELECT chunk_id, doc_id, embedding FROM vec_chunks")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var want []ChunkVectorResult
	for rows.Next() {
		var cid, did string
		var blob []byte
		if err := rows.Scan(&cid, &did, &blob); err != nil {
			t.Fatal(err)
		}
		vec := decodeFloat32s(blob)
		want = insertChunkSorted(want, ChunkVectorResult{ChunkID: cid, DocID: did, Score: CosineSimilarity(query, vec)}, 10)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i].Rank = i + 1
	}

	got, err := s.SearchChunks(query, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("len: want %d got %d", len(want), len(got))
	}
	for i := range want {
		if want[i].ChunkID != got[i].ChunkID {
			t.Fatalf("rank %d: want %s got %s", i, want[i].ChunkID, got[i].ChunkID)
		}
		if math.Abs(want[i].Score-got[i].Score) > 1e-6 {
			t.Errorf("score %s: want %.8f got %.8f", want[i].ChunkID, want[i].Score, got[i].Score)
		}
	}

	// Filtered: only doc-3 and doc-7 chunks, and the >100 cap survives.
	fGot, err := s.SearchChunksFiltered(query, []string{"doc-3", "doc-7"}, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fGot {
		if r.DocID != "doc-3" && r.DocID != "doc-7" {
			t.Errorf("filtered search returned %s (doc %s)", r.ChunkID, r.DocID)
		}
	}
	big := make([]string, 150)
	for i := range big {
		big[i] = docID(i)
	}
	if _, err := s.SearchChunksFiltered(query, big, 5); err != nil {
		t.Errorf("150-docID filter (cap 100) errored: %v", err)
	}
}

// TestCache_ExternalTxInvalidation characterizes the mechanism-2 contract:
// vec_chunks writes inside a CALLER-OWNED WriteTx are invisible to the
// Store until InvalidateChunkCache is called post-commit. Without it, the
// loaded cache serves stale rows; with it, the NEXT search reloads exactly
// once despite N writes, and a follow-up search does NOT reload again.
func TestCache_ExternalTxInvalidation(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 2)
	defer cleanup()

	query := []float32{0.5, -0.3, 0.8, 0.1, -0.6, 0.2, 0.9, -0.4}

	// Load (counter 1).
	if _, err := s.SearchChunks(query, 5); err != nil {
		t.Fatal(err)
	}
	if s.chunkCache.loadCount() != 1 {
		t.Fatalf("loadCount = %d", s.chunkCache.loadCount())
	}

	// N writes inside caller-owned txs WITHOUT invalidate → stale: the new
	// chunk is invisible to the cache even though it's in the DB.
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		return s.UpsertChunk(tx, "doc-0:chunk-new", "doc-0", []float32{1, 0, 0, 0, 0, 0, 0, 0})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.SearchChunks([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 50)
	for _, r := range got {
		if r.ChunkID == "doc-0:chunk-new" {
			t.Fatal("stale read expected but chunk visible — did invalidation semantics change?")
		}
	}
	if s.chunkCache.loadCount() != 1 {
		t.Fatal("caller-tx write must not reload the cache")
	}

	// More writes, then ONE invalidate → next search reloads once.
	for i := 0; i < 5; i++ {
		err := s.db.WriteTx(func(tx *sql.Tx) error {
			return s.UpsertChunk(tx, chunkID(200, i), "doc-200", []float32{0.9, 0.1, 0, 0, 0, 0, 0, 0})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s.InvalidateChunkCache()
	got, _ = s.SearchChunks([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 50)
	found := false
	for _, r := range got {
		if r.ChunkID == "doc-0:chunk-new" {
			found = true
		}
	}
	if !found {
		t.Error("post-invalidate reload missed the new chunk")
	}
	if s.chunkCache.loadCount() != 2 {
		t.Errorf("loadCount = %d, want 2 (one coalesced reload after N writes)", s.chunkCache.loadCount())
	}

	// Follow-up search on a clean cache: NO further reload.
	if _, err := s.SearchChunks(query, 5); err != nil {
		t.Fatal(err)
	}
	if s.chunkCache.loadCount() != 2 {
		t.Errorf("clean search reloaded: loadCount = %d, want 2", s.chunkCache.loadCount())
	}
}

// TestCache_DimensionMismatchGuard: a query whose length differs from the
// cache's row dimension — including a nil query from an embedder-less
// caller (the linter path) — matches nothing and must NOT panic. Preserved
// from the brute-force path's len(vec) != len(query) skip.
func TestCache_DimensionMismatchGuard(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 2)
	defer cleanup()

	got, err := s.Search(nil, 10)
	if err != nil || got != nil {
		t.Errorf("nil query: got=%v err=%v, want nil,nil", got, err)
	}
	got, err = s.Search([]float32{1, 2, 3}, 10)
	if err != nil || got != nil {
		t.Errorf("short query: got=%v err=%v, want nil,nil", got, err)
	}
	cgot, err := s.SearchChunks(nil, 10)
	if err != nil || cgot != nil {
		t.Errorf("nil chunk query: got=%v err=%v, want nil,nil", cgot, err)
	}
	fgot, err := s.SearchChunksFiltered(nil, []string{"doc-0"}, 10)
	if err != nil || fgot != nil {
		t.Errorf("nil filtered query: got=%v err=%v, want nil,nil", fgot, err)
	}
}

// TestCache_PatchDuringLoadAppliesAfter pins the ordering contract: a patch
// blocked on the write lock while a load is in flight applies AFTER the
// load completes — the final state contains both the loaded rows and the
// patched row, and exactly one load happened. (Same-package test: drives
// the cache mutex directly to create the interleaving deterministically.)
func TestCache_PatchDuringLoadAppliesAfter(t *testing.T) {
	s, cleanup := seededFixture(t, 10, 0)
	defer cleanup()

	// Hold the write lock so BOTH the first search (load) and the patch block.
	s.docCache.mu.Lock()

	searchDone := make(chan struct{})
	go func() {
		defer close(searchDone)
		s.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 3)
	}()
	patchDone := make(chan struct{})
	go func() {
		defer close(patchDone)
		s.Upsert("late-arrival", []float32{1, 0, 0, 0, 0, 0, 0, 0})
	}()

	// Let both goroutines block on the mutex, then release: the load must
	// complete first (the patch's write-lock acquisition queues behind it —
	// Upsert's WriteTx runs first but its cache patch waits for the load).
	time.Sleep(50 * time.Millisecond)
	s.docCache.mu.Unlock()

	<-searchDone
	<-patchDone

	if got := s.docCache.loadCount(); got != 1 {
		t.Errorf("loadCount = %d, want exactly 1 (patch must not trigger a second load)", got)
	}
	got, err := s.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ID != "late-arrival" {
		t.Errorf("patch-during-load lost: top-1 = %+v, want late-arrival", got)
	}
	if s.docCache.loadCount() != 1 {
		t.Errorf("loadCount drifted to %d after patch", s.docCache.loadCount())
	}
}

// TestCache_DimChangePatchInvalidates: a patch whose dimension differs from
// the cached rows (provider change mid-process) must INVALIDATE, not patch
// — patching would silently corrupt row alignment (Gate-8 MAJOR). The next
// search reloads from the DB and serves the new dimension coherently.
// NOTE: only the DOC cache has a production patch path; the chunk cache is
// invalidated explicitly (caller-tx design), so the dim guard in the shared
// upsert() is exercised there via the explicit-invalidate flow.
func TestCache_DimChangePatchInvalidates(t *testing.T) {
	s, cleanup := seededFixture(t, 5, 2)
	defer cleanup()

	// Load both caches (dim 8).
	s.Search([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 3)
	s.SearchChunks([]float32{1, 0, 0, 0, 0, 0, 0, 0}, 3)
	if s.docCache.loadCount() != 1 || s.chunkCache.loadCount() != 1 {
		t.Fatal("caches not loaded")
	}

	// Patch with a DIFFERENT dimension — must invalidate, not corrupt.
	s.Upsert("dim-changed", []float32{1, 0, 0, 0})
	if s.docCache.isLoaded() {
		t.Error("dim-mismatched doc patch did not invalidate")
	}
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		return s.UpsertChunk(tx, "doc-0:chunk-dimchanged", "doc-0", []float32{1, 0, 0, 0})
	})
	if err != nil {
		t.Fatal(err)
	}
	s.InvalidateChunkCache()

	// Next searches reload coherently from the DB (mixed dims: mismatched
	// rows skipped by the loader, as before).
	got, err := s.Search([]float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("post-invalidate search: %v", err)
	}
	if s.docCache.loadCount() != 2 {
		t.Errorf("doc loadCount = %d, want 2 (one coherent reload)", s.docCache.loadCount())
	}
	for _, r := range got {
		if r.ID == "dim-changed" {
			t.Error("dim-changed row served despite dimension mismatch with query")
		}
	}

	// Chunk cache: the explicit invalidate reloads coherently too.
	if _, err := s.SearchChunks([]float32{1, 0, 0, 0}, 10); err != nil {
		t.Fatalf("post-invalidate chunk search: %v", err)
	}
	if s.chunkCache.loadCount() != 2 {
		t.Errorf("chunk loadCount = %d, want 2", s.chunkCache.loadCount())
	}
}
