package hybrid

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestSearchStageHistograms(t *testing.T) {
	metrics.ResetForTest()
	s, _, _ := setupTest(t)
	if _, err := s.Search(SearchOpts{Query: "test", Limit: 5}, nil); err != nil {
		t.Fatal(err)
	}
	snap := metrics.Snapshot()
	stages := map[string]bool{}
	for i := 0; i+1 < len(snap); i += 2 {
		k, _ := snap[i].(string)
		switch k {
		case `search_duration_seconds{stage="bm25"}_count`:
			stages["bm25"] = true
		case `search_duration_seconds{stage="rrf"}_count`:
			stages["rrf"] = true
		}
	}
	if !stages["bm25"] {
		t.Error("bm25 stage not recorded")
	}
	if !stages["rrf"] {
		t.Error("rrf stage not recorded")
	}
	// nil queryVec → no vector stage
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == `search_duration_seconds{stage="vector"}_count` {
			t.Error("vector stage recorded with nil queryVec")
		}
	}
}
