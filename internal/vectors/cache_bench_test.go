package vectors

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// benchFixture builds a DB with `chunks` chunk vectors at the given dim.
// Test-only; 10K rows × 384 dims ≈ 15 MB of vectors.
func benchFixture(b *testing.B, chunks, dim int) (*Store, *storage.DB) {
	b.Helper()
	dir := b.TempDir()
	db, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	s := NewStore(db)

	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	const batch = 500
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
	return s, db
}

func benchQuery(dim int) []float32 {
	q := make([]float32, dim)
	for i := range q {
		q[i] = float32(i%17) / 17.0
	}
	return q
}

// BenchmarkBruteForceSearchChunks reproduces the PRE-CACHE implementation
// (SQL scan + BLOB decode + CosineSimilarity with per-vector norms) as the
// honest before/after baseline on the same machine and fixture.
func BenchmarkBruteForceSearchChunks(b *testing.B) {
	s, db := benchFixture(b, 10000, 384)
	defer db.Close()
	query := benchQuery(384)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		func() { // closure so defer rows.Close() fires per query, not per b.N (read pool caps at 4 conns)
			rows, err := db.ReadDB().Query("SELECT chunk_id, doc_id, embedding, dimensions FROM vec_chunks")
			if err != nil {
				b.Fatal(err)
			}
			defer rows.Close()
			var results []ChunkVectorResult
			for rows.Next() {
				var cid, did string
				var blob []byte
				var dims int
				if err := rows.Scan(&cid, &did, &blob, &dims); err != nil {
					b.Fatal(err)
				}
				vec := decodeFloat32s(blob)
				if len(vec) != len(query) {
					continue
				}
				results = insertChunkSorted(results, ChunkVectorResult{ChunkID: cid, DocID: did, Score: CosineSimilarity(query, vec)}, 20)
			}
			if err := rows.Err(); err != nil {
				b.Fatal(err)
			}
		}()
	}
	_ = s
}

// BenchmarkSearchChunks is the cache-backed AFTER path (first iteration
// includes the one-time load; steady state is what matters).
func BenchmarkSearchChunks(b *testing.B) {
	s, db := benchFixture(b, 10000, 384)
	defer db.Close()
	query := benchQuery(384)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SearchChunks(query, 20); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchDoc benchmarks the doc-level cache path.
func BenchmarkSearchDoc(b *testing.B) {
	dir := b.TempDir()
	db, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	s := NewStore(db)
	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	for i := 0; i < 2000; i++ {
		v := make([]float32, 384)
		for j := range v {
			v[j] = next()
		}
		if err := s.Upsert(docID(i), v); err != nil {
			b.Fatal(err)
		}
	}
	query := benchQuery(384)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search(query, 10); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBruteForceSearchDoc is the doc-level BEFORE baseline (spec test
// 6 requires doc-level before/after pairs, both recorded).
func BenchmarkBruteForceSearchDoc(b *testing.B) {
	dir := b.TempDir()
	db, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	s := NewStore(db)
	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	for i := 0; i < 2000; i++ {
		v := make([]float32, 384)
		for j := range v {
			v[j] = next()
		}
		if err := s.Upsert(docID(i), v); err != nil {
			b.Fatal(err)
		}
	}
	query := benchQuery(384)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		func() {
			rows, err := db.ReadDB().Query("SELECT id, embedding, dimensions FROM vec_entries")
			if err != nil {
				b.Fatal(err)
			}
			defer rows.Close()
			var results []VectorResult
			for rows.Next() {
				var id string
				var blob []byte
				var dims int
				if err := rows.Scan(&id, &blob, &dims); err != nil {
					b.Fatal(err)
				}
				vec := decodeFloat32s(blob)
				if len(vec) != len(query) {
					continue
				}
				results = insertSorted(results, VectorResult{ID: id, Score: CosineSimilarity(query, vec)}, 10)
			}
			if err := rows.Err(); err != nil {
				b.Fatal(err)
			}
		}()
	}
}
