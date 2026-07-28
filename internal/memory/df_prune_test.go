package memory

import (
	"database/sql"
	"fmt"
	"testing"
)

// V-M2d: a query term matching >20% of a >100-entry corpus is pruned from
// the FTS query; a query left with nothing keeps its first 3 terms.
func TestSearchDFPrunesFrequentTerms(t *testing.T) {
	_, store := setupTestDB(t)

	// 120 entries all containing "common"; one also contains "zebra".
	for i := 0; i < 120; i++ {
		content := fmt.Sprintf("common filler text number%d", i)
		if i == 0 {
			content = "common zebra migration"
		}
		store.Add(Entry{ID: fmt.Sprintf("doc%d", i), Content: content})
	}

	// "common" matches 120/120 (>20%) → pruned; "zebra" carries the query.
	results, err := store.Search("common zebra", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "doc0" {
		t.Fatalf("DF pruning failed: want only doc0 (zebra), got %d results", len(results))
	}
}

// Backstop: a query consisting only of over-frequent terms keeps its first
// 3 terms rather than returning nothing.
func TestSearchDFPruneBackstopKeepsFirstTerms(t *testing.T) {
	_, store := setupTestDB(t)

	for i := 0; i < 120; i++ {
		store.Add(Entry{ID: fmt.Sprintf("doc%d", i), Content: fmt.Sprintf("common shared text number%d", i)})
	}

	results, err := store.Search("common shared", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("all-frequent query must keep its first terms (backstop), got no results")
	}
}

// F-056: the chunk-leg probe (chunks_fts JOIN chunks_meta, DISTINCT docs)
// prunes too — a silent regression in that JOIN would fail open and
// disable chunk-leg pruning without this pin.
func TestSearchChunksDFPrunesFrequentTerms(t *testing.T) {
	db, _ := setupTestDB(t)
	cs := NewChunkStore(db)

	for i := 0; i < 120; i++ {
		content := fmt.Sprintf("common filler text number%d", i)
		if i == 0 {
			content = "common zebra migration"
		}
		docID := fmt.Sprintf("doc%d", i)
		if err := db.WriteTx(func(tx *sql.Tx) error {
			return cs.IndexChunks(tx, docID, []ChunkEntry{
				{ChunkID: docID + ":c0", ChunkIndex: 0, Content: content},
			})
		}); err != nil {
			t.Fatal(err)
		}
	}

	results, err := cs.SearchChunks("common zebra", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].DocID != "doc0" {
		t.Fatalf("chunk-leg DF pruning failed: want only doc0, got %d results", len(results))
	}
}

// Small corpora (≤100 entries) never prune — exact behavior preserved.
func TestSearchDFPruneSkipsSmallCorpus(t *testing.T) {
	_, store := setupTestDB(t)

	for i := 0; i < 50; i++ {
		content := fmt.Sprintf("common filler number%d", i)
		if i == 0 {
			content = "common zebra migration"
		}
		store.Add(Entry{ID: fmt.Sprintf("doc%d", i), Content: content})
	}

	results, err := store.Search("common zebra", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 50 {
		t.Fatalf("small corpus must not prune: want all 50 (common matches), got %d", len(results))
	}
}
