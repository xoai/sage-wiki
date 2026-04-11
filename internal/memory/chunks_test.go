package memory

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestChunkStore_IndexAndSearch(t *testing.T) {
	db := openTestDB(t)
	cs := NewChunkStore(db)

	// Index 3 docs with multiple chunks
	db.WriteTx(func(tx *sql.Tx) error {
		if err := cs.IndexChunks(tx, "doc1", []ChunkEntry{
			{ChunkID: "doc1-c0", ChunkIndex: 0, Heading: "Introduction", Content: "Go is a statically typed programming language"},
			{ChunkID: "doc1-c1", ChunkIndex: 1, Heading: "Concurrency", Content: "Goroutines enable lightweight concurrent execution"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := cs.IndexChunks(tx, "doc2", []ChunkEntry{
			{ChunkID: "doc2-c0", ChunkIndex: 0, Heading: "Overview", Content: "Rust is a systems programming language focused on safety"},
		}); err != nil {
			t.Fatal(err)
		}
		return cs.IndexChunks(tx, "doc3", []ChunkEntry{
			{ChunkID: "doc3-c0", ChunkIndex: 0, Heading: "Python Basics", Content: "Python is a dynamically typed programming language"},
			{ChunkID: "doc3-c1", ChunkIndex: 1, Heading: "Python Types", Content: "Python supports duck typing and gradual typing"},
		})
	})

	// Verify count
	count, err := cs.Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("expected 5 chunks, got %d", count)
	}

	// Search should find correct chunk by content
	results, err := cs.SearchChunks("goroutines concurrent", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'goroutines concurrent'")
	}
	if results[0].ChunkID != "doc1-c1" {
		t.Errorf("expected doc1-c1 as top result, got %s", results[0].ChunkID)
	}
	if results[0].DocID != "doc1" {
		t.Errorf("expected doc_id doc1, got %s", results[0].DocID)
	}

	// Search for programming language — should return multiple results
	results, err = cs.SearchChunks("programming language", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 3 {
		t.Errorf("expected at least 3 results for 'programming language', got %d", len(results))
	}
}

func TestChunkStore_DeleteDocChunks(t *testing.T) {
	db := openTestDB(t)
	cs := NewChunkStore(db)

	// Index chunks
	db.WriteTx(func(tx *sql.Tx) error {
		return cs.IndexChunks(tx, "doc1", []ChunkEntry{
			{ChunkID: "doc1-c0", ChunkIndex: 0, Content: "alpha beta gamma"},
			{ChunkID: "doc1-c1", ChunkIndex: 1, Content: "delta epsilon zeta"},
		})
	})

	// Delete
	db.WriteTx(func(tx *sql.Tx) error {
		return cs.DeleteDocChunks(tx, "doc1")
	})

	// Count should be 0
	count, err := cs.Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 chunks after delete, got %d", count)
	}

	// FTS5 search should return nothing
	results, err := cs.SearchChunks("alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results after delete, got %d", len(results))
	}
}

func TestChunkStore_MultiQuery(t *testing.T) {
	db := openTestDB(t)
	cs := NewChunkStore(db)

	db.WriteTx(func(tx *sql.Tx) error {
		cs.IndexChunks(tx, "doc1", []ChunkEntry{
			{ChunkID: "d1c0", ChunkIndex: 0, Content: "transformer attention mechanism"},
		})
		cs.IndexChunks(tx, "doc2", []ChunkEntry{
			{ChunkID: "d2c0", ChunkIndex: 0, Content: "flash attention optimization"},
		})
		return cs.IndexChunks(tx, "doc3", []ChunkEntry{
			{ChunkID: "d3c0", ChunkIndex: 0, Content: "recurrent neural network lstm"},
		})
	})

	// Multi-query should merge results
	results, err := cs.SearchChunksMultiQuery([]string{"attention", "neural network"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 3 {
		t.Errorf("expected at least 3 results from multi-query, got %d", len(results))
	}
}

func TestChunkStore_DocIDs(t *testing.T) {
	results := []ChunkResult{
		{DocID: "doc1"},
		{DocID: "doc2"},
		{DocID: "doc1"},
		{DocID: "doc3"},
	}
	ids := DocIDs(results)
	if len(ids) != 3 {
		t.Errorf("expected 3 unique doc IDs, got %d", len(ids))
	}
}
