//go:build unix

package vectors

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// TestResidentCeiling is the SPEC-06 executable memory gate, gated to unix
// (the only platforms where mapFile is a real mmap — the fallback has no
// memory win by design): after loading both backends over the same large
// fixture, the mmap backend's heap must be < 15% of the memory backend's.
func TestResidentCeiling(t *testing.T) {
	const n, dim = 50000, 384 // ~77 MB fp32 matrix
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "big.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	const batch = 1000
	for start := 0; start < n; start += batch {
		if err := db.WriteTx(func(tx *sql.Tx) error {
			for i := start; i < start+batch && i < n; i++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = next()
				}
				if _, err := tx.Exec(
					"INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)",
					chunkID(0, i), encodeFloat32s(v), dim,
				); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteIndexFile(db, IndexTableDocs, filepath.Join(dir, docIndexFile), QuantNone); err != nil {
		t.Fatal(err)
	}

	heapAfter := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}

	// Memory backend: full matrix lands on the Go heap.
	mem := NewStore(db)
	base := heapAfter()
	if _, err := mem.Search(benchQuery(dim), 10); err != nil {
		t.Fatal(err)
	}
	memHeap := heapAfter() - base
	runtime.KeepAlive(mem)

	// Fresh DB handle so the mmap store shares nothing with the first one.
	db2, err := storage.Open(filepath.Join(dir, "big.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	mm := NewStore(db2, WithVectorBackend(backendMmap), WithIndexDir(dir))
	defer mm.Close()
	base2 := heapAfter()
	if _, err := mm.Search(benchQuery(dim), 10); err != nil {
		t.Fatal(err)
	}
	mmapHeap := heapAfter() - base2

	ratio := float64(mmapHeap) / float64(memHeap)
	t.Logf("resident heap: memory backend %d bytes, mmap backend %d bytes (ratio %.4f)",
		memHeap, mmapHeap, ratio)
	if ratio >= 0.15 {
		t.Errorf("mmap heap ratio = %.4f, want < 0.15 of the memory backend", ratio)
	}
}
