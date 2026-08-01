package vectors

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// mmapBenchFixture builds the standard 10K×384 chunk fixture plus fp32
// index files, returning a memory-backed Store and the index dir.
func mmapBenchFixture(b *testing.B) (*Store, string) {
	b.Helper()
	dir := b.TempDir()
	db, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	s := NewStore(db)

	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	const chunks, dim, batch = 10000, 384, 500
	for start := 0; start < chunks; start += batch {
		if err := db.WriteTx(func(tx *sql.Tx) error {
			for i := start; i < start+batch && i < chunks; i++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = next()
				}
				if err := s.UpsertChunk(tx, chunkID(0, i), "doc-bench", v); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
	}
	if _, err := WriteIndexFile(db, IndexTableDocs, filepath.Join(dir, docIndexFile), QuantNone); err != nil {
		b.Fatal(err)
	}
	if _, err := WriteIndexFile(db, IndexTableChunks, filepath.Join(dir, chunkIndexFile), QuantNone); err != nil {
		b.Fatal(err)
	}
	return s, dir
}

// BenchmarkSearchWarm_Memory is the SPEC-06 warm baseline: repeated chunk
// searches against the loaded in-memory cache.
func BenchmarkSearchWarm_Memory(b *testing.B) {
	s, _ := mmapBenchFixture(b)
	q := benchQuery(384)
	if _, err := s.SearchChunks(q, 20); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchChunks(q, 20); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchWarm_Mmap: repeated chunk searches against the mmap
// snapshot (warm page cache). SPEC-06 gate: within 2x of _Memory
// (scripts/spec06/warmcheck.sh).
func BenchmarkSearchWarm_Mmap(b *testing.B) {
	s, dir := mmapBenchFixture(b)
	mm := NewStore(s.db, WithVectorBackend(backendMmap), WithIndexDir(dir))
	b.Cleanup(func() { _ = mm.Close() })
	q := benchQuery(384)
	if _, err := mm.SearchChunks(q, 20); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := mm.SearchChunks(q, 20); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchCold_Memory: a fresh Store's first search (full cache
// load from SQLite) each iteration.
func BenchmarkSearchCold_Memory(b *testing.B) {
	s, _ := mmapBenchFixture(b)
	q := benchQuery(384)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fresh := NewStore(s.db)
		b.StartTimer()
		if _, err := fresh.SearchChunks(q, 20); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchCold_Mmap: a fresh Store's first search (map + parse +
// probe, cold page cache pressure aside) each iteration — the SPEC-06
// "cold search on a just-opened workspace" measurement.
func BenchmarkSearchCold_Mmap(b *testing.B) {
	s, dir := mmapBenchFixture(b)
	q := benchQuery(384)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fresh := NewStore(s.db, WithVectorBackend(backendMmap), WithIndexDir(dir))
		b.StartTimer()
		if _, err := fresh.SearchChunks(q, 20); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		_ = fresh.Close()
		b.StartTimer()
	}
}
