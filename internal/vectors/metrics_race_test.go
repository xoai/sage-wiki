package vectors

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/storage"
)

// TestConcurrentFirstSearchExactlyOneMiss pins the double-checked-locking
// counter placement (spec §2): N concurrent first-searchers produce exactly
// ONE miss (the actual reload) — the write-lock-after-recheck gate.
func TestConcurrentFirstSearchExactlyOneMiss(t *testing.T) {
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
	s.InvalidateChunkCache() // ensure chunk cache cold; doc cache also cold (never loaded)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Search([]float32{1, 0}, 5)
		}()
	}
	wg.Wait()

	if got := cacheSnap(`vector_cache_misses_total{cache="doc"}`); got != 1 {
		t.Errorf("doc misses = %d, want exactly 1 under concurrent first search", got)
	}
}
