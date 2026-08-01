package vectors

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// recallFixture builds a deterministic DB with n doc vectors at dim, using
// the same LCG scheme as benchFixture (seed 7).
func recallFixture(t *testing.T, n, dim int) (*Store, *storage.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewStore(db)

	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = next()
		}
		if err := s.Upsert(chunkID(0, i), v); err != nil {
			t.Fatal(err)
		}
	}
	return s, db, dir
}

// TestInt8RecallAt10 is the SPEC-06 executable recall gate: int8 global-
// scale quantization must keep recall@10 ≥ 0.95 vs the fp32 baseline on
// the seeded fixture. Deterministic — no timing, no flake.
func TestInt8RecallAt10(t *testing.T) {
	const n, dim, queries = 2000, 128, 50
	s, db, dir := recallFixture(t, n, dim)

	if _, err := WriteIndexFile(db, IndexTableDocs, filepath.Join(dir, docIndexFile), QuantInt8); err != nil {
		t.Fatal(err)
	}
	mm := mmapStore(db, dir)
	t.Cleanup(func() { _ = mm.Close() })

	rng := uint32(13)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	var recallSum float64
	for qi := 0; qi < queries; qi++ {
		q := make([]float32, dim)
		for j := range q {
			q[j] = next()
		}
		exact, err := s.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		approx, err := mm.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, r := range exact {
			set[r.ID] = true
		}
		hits := 0
		for _, r := range approx {
			if set[r.ID] {
				hits++
			}
		}
		recallSum += float64(hits) / 10.0
	}
	recall := recallSum / queries
	t.Logf("recall@10 (int8 vs fp32, %d queries over %dx%d) = %.4f", queries, n, dim, recall)
	if recall < 0.95 {
		t.Errorf("recall@10 = %.4f, want >= 0.95", recall)
	}
	// Anti-vacuity (F-053): a silent fallback to the memory path would
	// score a perfect 1.0 — prove the snapshot actually served.
	if got := mm.MmapServedCount(); got != queries {
		t.Errorf("MmapServedCount = %d, want %d (recall measured the memory path!)", got, queries)
	}
	if mm.docCache.isLoaded() {
		t.Error("recall test must not load the in-memory cache")
	}
}
