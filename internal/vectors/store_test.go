package vectors

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) (*storage.DB, *Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, NewStore(db)
}

func TestUpsertAndSearch(t *testing.T) {
	_, store := setupTestDB(t)

	// Insert vectors
	store.Upsert("v1", []float32{1, 0, 0})
	store.Upsert("v2", []float32{0, 1, 0})
	store.Upsert("v3", []float32{0.9, 0.1, 0})

	// Search for vector closest to [1, 0, 0]
	results, err := store.Search([]float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// v1 should be first (exact match), v3 second (close), v2 last
	if results[0].ID != "v1" {
		t.Errorf("expected v1 first, got %s", results[0].ID)
	}
	if results[1].ID != "v3" {
		t.Errorf("expected v3 second, got %s", results[1].ID)
	}
	if results[0].Score < 0.99 {
		t.Errorf("expected score ~1.0 for exact match, got %f", results[0].Score)
	}
	if results[0].Rank != 1 {
		t.Errorf("expected rank 1, got %d", results[0].Rank)
	}
}

func TestUpsertOverwrite(t *testing.T) {
	_, store := setupTestDB(t)

	store.Upsert("v1", []float32{1, 0, 0})
	store.Upsert("v1", []float32{0, 1, 0}) // overwrite

	results, _ := store.Search([]float32{0, 1, 0}, 1)
	if results[0].ID != "v1" || results[0].Score < 0.99 {
		t.Error("upsert should overwrite existing vector")
	}
}

func TestDelete(t *testing.T) {
	_, store := setupTestDB(t)

	store.Upsert("v1", []float32{1, 0, 0})
	store.Delete("v1")

	count, _ := store.Count()
	if count != 0 {
		t.Errorf("expected 0 after delete, got %d", count)
	}
}

func TestDimensionMismatchSkipped(t *testing.T) {
	_, store := setupTestDB(t)

	store.Upsert("v1", []float32{1, 0, 0})       // 3-dim
	store.Upsert("v2", []float32{1, 0, 0, 0, 0})  // 5-dim

	// Search with 3-dim query should only match v1
	results, _ := store.Search([]float32{1, 0, 0}, 10)
	if len(results) != 1 {
		t.Errorf("expected 1 result (dim mismatch skipped), got %d", len(results))
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		a, b     []float32
		expected float64
	}{
		{[]float32{1, 0}, []float32{1, 0}, 1.0},
		{[]float32{1, 0}, []float32{0, 1}, 0.0},
		{[]float32{1, 0}, []float32{-1, 0}, -1.0},
		{[]float32{1, 1}, []float32{1, 1}, 1.0},
		{[]float32{0, 0}, []float32{1, 0}, 0.0}, // zero vector
	}
	for _, tt := range tests {
		score := CosineSimilarity(tt.a, tt.b)
		if math.Abs(score-tt.expected) > 0.001 {
			t.Errorf("CosineSimilarity(%v, %v) = %f, want %f", tt.a, tt.b, score, tt.expected)
		}
	}
}

func TestCountAndDimensions(t *testing.T) {
	_, store := setupTestDB(t)

	count, _ := store.Count()
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	store.Upsert("v1", []float32{1, 0, 0})
	store.Upsert("v2", []float32{0, 1, 0})

	count, _ = store.Count()
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}

	dims, _ := store.Dimensions()
	if dims != 3 {
		t.Errorf("expected 3 dimensions, got %d", dims)
	}
}

func TestEncodeDecodRoundtrip(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 99.99}
	encoded := encodeFloat32s(original)
	decoded := decodeFloat32s(encoded)

	for i := range original {
		if math.Abs(float64(original[i]-decoded[i])) > 0.0001 {
			t.Errorf("roundtrip mismatch at %d: %f vs %f", i, original[i], decoded[i])
		}
	}
}

func TestChunkVectorUpsertAndSearch(t *testing.T) {
	db, store := setupTestDB(t)

	// Insert chunk vectors via transaction
	db.WriteTx(func(tx *sql.Tx) error {
		store.UpsertChunk(tx, "c1", "doc1", []float32{1, 0, 0})
		store.UpsertChunk(tx, "c2", "doc1", []float32{0.9, 0.1, 0})
		store.UpsertChunk(tx, "c3", "doc2", []float32{0, 1, 0})
		return nil
	})

	// Brute-force search
	results, err := store.SearchChunks([]float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("expected c1 first, got %s", results[0].ChunkID)
	}
	if results[0].DocID != "doc1" {
		t.Errorf("expected doc1, got %s", results[0].DocID)
	}
}

func TestChunkVectorFilteredSearch(t *testing.T) {
	db, store := setupTestDB(t)

	db.WriteTx(func(tx *sql.Tx) error {
		store.UpsertChunk(tx, "c1", "doc1", []float32{1, 0, 0})
		store.UpsertChunk(tx, "c2", "doc2", []float32{0.9, 0.1, 0})
		store.UpsertChunk(tx, "c3", "doc3", []float32{0.8, 0.2, 0})
		return nil
	})

	// Filtered search should only return chunks from doc1
	results, err := store.SearchChunksFiltered([]float32{1, 0, 0}, []string{"doc1"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (filtered to doc1), got %d", len(results))
	}
	if results[0].ChunkID != "c1" {
		t.Errorf("expected c1, got %s", results[0].ChunkID)
	}

	// Empty doc IDs returns nil
	results, err = store.SearchChunksFiltered([]float32{1, 0, 0}, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil for empty docIDs, got %d results", len(results))
	}
}

func TestDeleteDocChunkVectors(t *testing.T) {
	db, store := setupTestDB(t)

	db.WriteTx(func(tx *sql.Tx) error {
		store.UpsertChunk(tx, "c1", "doc1", []float32{1, 0, 0})
		store.UpsertChunk(tx, "c2", "doc1", []float32{0, 1, 0})
		return nil
	})

	store.DeleteDocChunkVectors("doc1")

	results, _ := store.SearchChunks([]float32{1, 0, 0}, 10)
	if len(results) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(results))
	}
}
