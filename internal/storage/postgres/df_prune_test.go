package postgres

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/xoai/sage-wiki/internal/store"
)

// pg twin of V-M2d: over-frequent terms are pruned on >100-entry corpora;
// all-frequent queries keep their first terms.
func TestPGSearchDFPrunesFrequentTerms(t *testing.T) {
	b, _, cleanup := derivedTestBackend(t)
	defer cleanup()

	es := b.Entries()
	for i := 0; i < 120; i++ {
		content := fmt.Sprintf("common filler text number%d", i)
		if i == 0 {
			content = "common zebra migration"
		}
		if err := es.Add(store.Entry{ID: fmt.Sprintf("doc%d", i), Content: content}); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	results, err := es.Search("common zebra", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "doc0" {
		t.Fatalf("pg DF pruning failed: want only doc0, got %d results", len(results))
	}

	backstop, err := es.Search("common filler", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(backstop) == 0 {
		t.Fatal("pg all-frequent query must keep first terms (backstop)")
	}
}

// PB-2: pg twin of the chunk-leg DF-prune pin. Like the sqlite twin, the
// chunk leg probes the DOCUMENT corpus (`entries`), so both fusion legs prune
// an identical term set by construction — and the probe is a plain count
// rather than a COUNT(DISTINCT) over the chunk tables.
func TestPGSearchChunksDFPrunesFrequentTerms(t *testing.T) {
	b, _, cleanup := derivedTestBackend(t)
	defer cleanup()

	es := b.Entries()
	cs := b.Chunks()
	for i := 0; i < 120; i++ {
		content := fmt.Sprintf("common filler text number%d", i)
		if i == 0 {
			content = "common zebra migration"
		}
		docID := fmt.Sprintf("doc%d", i)
		if err := es.Add(store.Entry{ID: docID, Content: content}); err != nil {
			t.Fatal(err)
		}
		if err := b.WriteTx(func(tx *sql.Tx) error {
			return cs.IndexChunks(tx, docID, []store.ChunkEntry{
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
		t.Fatalf("pg chunk-leg DF pruning failed: want only doc0, got %d results", len(results))
	}

	docHits, err := es.Search("common zebra", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(docHits) != 1 || docHits[0].ID != "doc0" {
		t.Fatalf("pg doc leg pruned differently from the chunk leg: got %d results", len(docHits))
	}
}

// pg twin of V-M2e: with the v6 setweight tsvector, a single id/article_path
// (weight A) match outranks a high-tf content-only (weight D) match.
func TestPGSearchColumnWeights(t *testing.T) {
	b, _, cleanup := derivedTestBackend(t)
	defer cleanup()

	es := b.Entries()
	if err := es.Add(store.Entry{ID: "concept:gopher", Content: "a burrowing rodent of note", ArticlePath: "wiki/concepts/gopher.md"}); err != nil {
		t.Fatal(err)
	}
	if err := es.Add(store.Entry{ID: "concept:rodent", Content: "the gopher digs gopher tunnels where gopher families raise gopher pups", ArticlePath: "wiki/concepts/rodent.md"}); err != nil {
		t.Fatal(err)
	}

	results, err := es.Search("gopher", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected both docs, got %+v", results)
	}
	if results[0].ID != "concept:gopher" {
		t.Errorf("weight-A id/path match must outrank content-only tf, got %s first", results[0].ID)
	}
}
