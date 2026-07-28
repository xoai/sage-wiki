package memory

import (
	"fmt"
	"testing"
)

// F-067: GetSourceDates batches its IN clause — >999 IDs (past sqlite's
// historical parameter limits) must round-trip without error.
func TestGetSourceDatesBatchesLargeIDSets(t *testing.T) {
	_, store := setupTestDB(t)

	ids := make([]string, 1200)
	for i := range ids {
		ids[i] = fmt.Sprintf("doc%d", i)
		if i%3 == 0 {
			if err := store.SetSourceDate(ids[i], int64(1000000+i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	dates, err := store.GetSourceDates(ids)
	if err != nil {
		t.Fatalf("batched lookup failed: %v", err)
	}
	if len(dates) != 400 {
		t.Errorf("got %d dates, want 400", len(dates))
	}
	if dates["doc300"] != 1000300 {
		t.Errorf("doc300 = %d, want 1000300", dates["doc300"])
	}
}

// V-M2e (sqlite half): under the column weights bm25(entries, 3.0, 1.0,
// 1.5, 3.0), a match in id/article_path (the title proxies) outranks an
// equal match that appears only in content.
func TestSearchColumnWeightsTitleProxyOutranksContent(t *testing.T) {
	_, store := setupTestDB(t)

	// The content-only doc mentions "gopher" four times (high tf) — under
	// uniform column weights it outranks the title-proxy doc, so only the
	// 3.0 id/article_path weighting can flip this order.
	store.Add(Entry{ID: "concept:gopher", Content: "a burrowing rodent of note", ArticlePath: "wiki/concepts/gopher.md"})
	store.Add(Entry{ID: "concept:rodent", Content: "the gopher digs gopher tunnels where gopher families raise gopher pups", ArticlePath: "wiki/concepts/rodent.md"})

	results, err := store.Search("gopher", nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected both docs, got %+v", results)
	}
	if results[0].ID != "concept:gopher" {
		t.Errorf("id/article_path match must outrank content-only match, got %s first", results[0].ID)
	}
}
