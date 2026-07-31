package vectors

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
)

func annTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedCorpus inserts n deterministic dim-vectors via the Store (DB write
// path), returning the rng for probe generation.
func seedCorpus(t *testing.T, s *Store, n, dim int, seed int64) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		vec := make([]float32, dim)
		for j := range vec {
			vec[j] = rng.Float32()*2 - 1
		}
		if err := s.Upsert(fmt.Sprintf("doc-%d", i), vec); err != nil {
			t.Fatal(err)
		}
	}
}

func probe(rng *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for j := range v {
		v[j] = rng.Float32()*2 - 1
	}
	return v
}

func TestANN_RecallParity(t *testing.T) {
	db := annTestDB(t)
	// n=1000 (was 2000): the gate is recall ≥9/10 vs brute force, and the
	// margin GROWS at smaller n (top-10 is 1% of corpus vs 0.5%) while the
	// HNSW stays multi-layer — same coverage, half the seed+build cost.
	const n, dim, probes = 1000, 64, 20

	brute := NewStore(db)
	seedCorpus(t, brute, n, dim, 42)

	ann := NewStore(db, WithANN(true))
	if ann.IndexKind() != "hnsw" {
		t.Fatalf("IndexKind = %q, want hnsw", ann.IndexKind())
	}

	rng := rand.New(rand.NewSource(7))
	var annDur, bruteDur time.Duration
	for p := 0; p < probes; p++ {
		q := probe(rng, dim)

		start := time.Now()
		bres, err := brute.Search(q, 10)
		bruteDur += time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		ares, err := ann.Search(q, 10)
		annDur += time.Since(start)
		if err != nil {
			t.Fatal(err)
		}

		top := map[string]bool{}
		for _, r := range bres {
			top[r.ID] = true
		}
		overlap := 0
		for _, r := range ares {
			if top[r.ID] {
				overlap++
			}
		}
		if overlap < 9 {
			t.Errorf("probe %d: recall %d/10, want >= 9", p, overlap)
		}
	}
	t.Logf("benchmark (not gated): %d probes over %dx%d — brute %v, hnsw %v", probes, n, dim, bruteDur, annDur)
}

func TestANN_DimensionGuard(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))
	seedCorpus(t, s, 10, 8, 1)
	res, err := s.Search(make([]float32, 16), 5) // wrong dim
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Errorf("dim-mismatched query returned %v, want nil", res)
	}
}

func TestANN_DeleteAndInvalidation(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))
	seedCorpus(t, s, 50, 8, 2)

	q := make([]float32, 8)
	copy(q, []float32{1, 0, 0, 0, 0, 0, 0, 0})
	s.Upsert("target", []float32{1, 0, 0, 0, 0, 0, 0, 0})

	res, _ := s.Search(q, 5)
	found := false
	for _, r := range res {
		if r.ID == "target" {
			found = true
		}
	}
	if !found {
		t.Fatal("target not found before delete")
	}
	if err := s.Delete("target"); err != nil {
		t.Fatal(err)
	}
	res, _ = s.Search(q, 5)
	for _, r := range res {
		if r.ID == "target" {
			t.Error("deleted id still in ANN results")
		}
	}
}

func TestANN_FilteredSearch(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))

	// Two docs with many chunks each; the filter must restrict hits to one.
	rng := rand.New(rand.NewSource(3))
	tx, err := db.WriteDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for d := 0; d < 2; d++ {
		for c := 0; c < 100; c++ {
			vec := make([]float32, 8)
			for j := range vec {
				vec[j] = rng.Float32()
			}
			if err := s.UpsertChunk(tx, fmt.Sprintf("doc%d:chunk%d", d, c), fmt.Sprintf("doc%d", d), vec); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Caller-tx writes become visible after the invalidation contract.
	s.InvalidateChunkCache()
	// High-selectivity filter: only doc0 is allowed — all results must be doc0's.
	res, err := s.SearchChunksFiltered(probe(rng, 8), []string{"doc0"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.DocID != "doc0" {
			t.Errorf("filtered search leaked %s (doc %s)", r.ChunkID, r.DocID)
		}
	}
}

func TestANN_UpsertChunkRebuildsViaInvalidation(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))

	// Caller-tx write (UpsertChunk takes the caller's tx — the Store can't
	// observe the commit; the graph updates only via invalidation+rebuild).
	tx, err := db.WriteDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChunk(tx, "c1", "doc1", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	tx.Commit()

	s.InvalidateChunkCache()
	res, err := s.SearchChunks([]float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res {
		if r.ChunkID == "c1" {
			found = true
		}
	}
	if !found {
		t.Error("chunk not found after invalidation + rebuild")
	}
}

func TestANN_BruteForceUnchanged(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db) // no option — default path
	seedCorpus(t, s, 20, 8, 4)
	res, err := s.Search(probe(rand.New(rand.NewSource(5)), 8), 5)
	if err != nil || len(res) == 0 {
		t.Errorf("default brute-force path broken: %v %v", res, err)
	}
	if s.IndexKind() != "brute-force" {
		t.Errorf("IndexKind = %q, want brute-force", s.IndexKind())
	}
}

// Regression (gate review, CRITICAL): a deleted non-last row must not
// corrupt the moved row's graph node after a later insert. Pre-fix,
// rebuild aliased mat subslices; remove()'s swap-truncate left the moved
// id's node pointing at the tail slot, and append overwrote it in place.
func TestANN_DeleteThenInsertKeepsMovedRow(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))

	vecA := []float32{1, 0, 0, 0}
	vecB := []float32{0, 1, 0, 0}
	vecC := []float32{0, 0, 1, 0}
	s.Upsert("a", vecA)
	s.Upsert("b", vecB)
	s.Upsert("c", vecC)

	// Force a rebuild from DB — the path that (pre-fix) aliased mat rows.
	if _, err := s.Search(vecA, 1); err != nil {
		t.Fatal(err)
	}

	// Delete a NON-last row: c's row swap-moves into b's slot and the tail
	// slot is freed for the next append.
	if err := s.Delete("b"); err != nil {
		t.Fatal(err)
	}
	// Append reuses the freed tail capacity — pre-fix this overwrote c's
	// aliased graph vector with d's bytes.
	s.Upsert("d", []float32{0, 0, 0, 1})

	// c must still resolve to ITS vector.
	res, err := s.Search(vecC, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ID != "c" {
		t.Fatalf("self-query of moved row = %+v, want c first (aliasing desync)", res)
	}
}

// Extreme selectivity (spec R3): the eligible doc's chunks all rank
// OUTSIDE the limit*4 over-fetch window, so the filtered result may be
// shorter than limit — the documented divergence, asserted here.
func TestANN_FilteredSearch_ExtremeSelectivity(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))
	tx, _ := db.WriteDB().Begin()

	// 200 "near" chunks in doc0 and 3 "far" chunks in doc1; the query
	// aligns with doc0's direction so doc1's chunks rank below the
	// limit*4 = 40 window.
	q := make([]float32, 8)
	q[0] = 1
	for i := 0; i < 200; i++ {
		v := make([]float32, 8)
		v[0] = 1
		v[1] = float32(i) * 0.0001
		if err := s.UpsertChunk(tx, itoa2(i), "doc0", v); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		v := make([]float32, 8)
		v[2] = 1 // orthogonal — ranks last
		if err := s.UpsertChunk(tx, "far"+itoa2(i), "doc1", v); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	s.InvalidateChunkCache()

	res, err := s.SearchChunksFiltered(q, []string{"doc1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) > 3 {
		t.Errorf("filtered result = %d rows, want <= 3 (over-fetch window missed doc1)", len(res))
	}
	for _, r := range res {
		if r.DocID != "doc1" {
			t.Errorf("leaked doc %s", r.DocID)
		}
	}
}

func itoa2(n int) string {
	digits := []byte{}
	if n == 0 {
		return "0"
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestANN_ZeroNormVectorSkipped(t *testing.T) {
	db := annTestDB(t)
	s := NewStore(db, WithANN(true))
	s.Upsert("real", []float32{1, 0, 0, 0})
	s.Upsert("zero", []float32{0, 0, 0, 0})
	res, err := s.Search([]float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.ID == "zero" {
			t.Error("zero-norm vector returned with NaN score — must be skipped")
		}
		if r.Score != r.Score {
			t.Errorf("NaN score leaked for %s", r.ID)
		}
	}
}
