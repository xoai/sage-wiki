package postgres

import (
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
