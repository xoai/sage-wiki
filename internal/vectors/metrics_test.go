package vectors

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/storage"
)

func cacheSnap(key string) int64 {
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == key {
			return snap[i+1].(int64)
		}
	}
	return 0
}

func TestCacheHitMissMetrics(t *testing.T) {
	metrics.ResetForTest()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := NewStore(db)

	if err := s.Upsert("d1", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	s.InvalidateChunkCache() // force chunk cache unloaded; doc cache loaded by Upsert patch? Upsert patches cache only when loaded

	// First search triggers the load → miss, no hit.
	if _, err := s.Search([]float32{1, 0}, 5); err != nil {
		t.Fatal(err)
	}
	if got := cacheSnap(`vector_cache_misses_total{cache="doc"}`); got != 1 {
		t.Errorf("doc misses = %d, want 1", got)
	}
	if got := cacheSnap(`vector_cache_hits_total{cache="doc"}`); got != 0 {
		t.Errorf("doc hits = %d, want 0 on first search", got)
	}

	// Second search served from loaded cache → hit.
	if _, err := s.Search([]float32{1, 0}, 5); err != nil {
		t.Fatal(err)
	}
	if got := cacheSnap(`vector_cache_hits_total{cache="doc"}`); got != 1 {
		t.Errorf("doc hits = %d, want 1 on second search", got)
	}
	if got := cacheSnap(`vector_cache_misses_total{cache="doc"}`); got != 1 {
		t.Errorf("doc misses = %d, want still 1", got)
	}
}
