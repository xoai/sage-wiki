package search

import (
	"database/sql"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

func indexTestChunks(t *testing.T, db *storage.DB, cs *memory.ChunkStore, docID string, chunks []memory.ChunkEntry) {
	t.Helper()
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return cs.IndexChunks(tx, docID, chunks)
	}); err != nil {
		t.Fatalf("index chunks: %v", err)
	}
}

func TestEnhancedSearch_BasicChunkSearch(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "goroutines are lightweight threads"})
	ms.Add(memory.Entry{ID: "doc2", Content: "python asyncio event loop"})

	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Heading: "Goroutines", Content: "goroutines enable concurrent programming in go"},
		{ChunkID: "doc1:c1", ChunkIndex: 1, Heading: "Channels", Content: "channels provide communication between goroutines"},
	})
	indexTestChunks(t, db, cs, "doc2", []memory.ChunkEntry{
		{ChunkID: "doc2:c0", ChunkIndex: 0, Heading: "Asyncio", Content: "python asyncio provides event loop for async io"},
	})

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:          "goroutines concurrent",
		Limit:          5,
		ChunkStore:     cs,
		MemStore:       ms,
		VecStore:       vs,
		QueryExpansion: false,
		RerankEnabled:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if results[0].DocID != "doc1" {
		t.Errorf("expected doc1 as top result, got %s", results[0].DocID)
	}
}

func TestEnhancedSearch_Deduplication(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "keyword keyword keyword"})

	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "keyword alpha"},
		{ChunkID: "doc1:c1", ChunkIndex: 1, Content: "keyword beta"},
		{ChunkID: "doc1:c2", ChunkIndex: 2, Content: "keyword gamma"},
	})

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:          "keyword",
		Limit:          10,
		ChunkStore:     cs,
		MemStore:       ms,
		VecStore:       vs,
		QueryExpansion: false,
		RerankEnabled:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result after dedup, got %d", len(results))
	}
	if results[0].DocID != "doc1" {
		t.Errorf("expected doc1, got %s", results[0].DocID)
	}
}

func TestEnhancedSearch_NoExpansionNoRerank(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "test content"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "test content here"},
	})

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:          "test",
		Limit:          5,
		ChunkStore:     cs,
		MemStore:       ms,
		VecStore:       vs,
		QueryExpansion: false,
		RerankEnabled:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected results even without expansion or rerank")
	}
}

func TestEnhancedSearch_EmptyIndex(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	results, err := EnhancedSearch(EnhancedSearchOpts{
		Query:          "anything",
		Limit:          5,
		ChunkStore:     cs,
		MemStore:       ms,
		VecStore:       vs,
		QueryExpansion: false,
		RerankEnabled:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty index, got %d", len(results))
	}
}
