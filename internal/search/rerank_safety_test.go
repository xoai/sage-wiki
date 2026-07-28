package search

import (
	"math"
	"testing"
)

// V-M1d prerequisites: the parser must distinguish "scored 0" from
// "not scored at all" — the old []float64 return conflated them.
func TestParseRerankJSON_TracksScoredEntries(t *testing.T) {
	input := `[{"id": 2, "score": 7}, {"id": 4, "score": 0}]`
	scores, err := parseRerankJSON(input, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 5 {
		t.Fatalf("len = %d, want 5", len(scores))
	}
	for i, want := range []bool{false, true, false, true, false} {
		if (scores[i] != nil) != want {
			t.Errorf("scores[%d] scored = %v, want %v", i, scores[i] != nil, want)
		}
	}
	if *scores[1] != 7 {
		t.Errorf("scores[1] = %v, want 7", *scores[1])
	}
	if *scores[3] != 0 {
		t.Errorf("scores[3] = %v, want 0 (a genuine zero score, not unscored)", *scores[3])
	}
}

func TestNormalizeRelevance(t *testing.T) {
	rels := NormalizeRelevance([]float64{0.03, 0.02, 0.01})
	want := []float64{1.0, 0.5, 0.0}
	for i := range want {
		if math.Abs(rels[i]-want[i]) > 1e-9 {
			t.Errorf("rels[%d] = %v, want %v", i, rels[i], want[i])
		}
	}

	// Degenerate: all-equal scores normalize to 1.0 (order preserved, no /0).
	one := NormalizeRelevance([]float64{0.5})
	if len(one) != 1 || one[0] != 1.0 {
		t.Errorf("single-value normalize = %v, want [1.0]", one)
	}
	if got := NormalizeRelevance(nil); len(got) != 0 {
		t.Errorf("nil normalize = %v, want empty", got)
	}
}

// V-M1d (coverage gate): the LLM scoring 1 of 15 candidates must NOT blend —
// sage-memory measured a 25-50pp R@1 regression from exactly this shape.
func TestBlendReranked_CoverageGateSkips(t *testing.T) {
	rels := make([]float64, 15)
	reranked := make([]RerankResult, 15)
	for i := range reranked {
		rels[i] = 1.0 - float64(i)*0.05
		reranked[i] = RerankResult{ID: "d", RetrievalRank: i + 1}
	}
	reranked[7].Scored = true
	reranked[7].Score = 0.9

	_, applied := BlendReranked(rels, reranked, 0.5)
	if applied {
		t.Error("blend applied at 1/15 coverage — the coverage gate must skip it")
	}
}

// V-M1d (None-passthrough on the right scale): an unscored candidate with
// normalized relevance 0.9 must outrank a scored candidate blended to ≤0.5.
func TestBlendReranked_UnscoredKeepsNormalizedRelevance(t *testing.T) {
	rels := []float64{0.9, 0.4}
	reranked := []RerankResult{
		{ID: "a", RetrievalRank: 1, Scored: false},
		{ID: "b", RetrievalRank: 2, Scored: true, Score: 0.4},
	}

	finals, applied := BlendReranked(rels, reranked, 0.5)
	if !applied {
		t.Fatal("expected blend to apply at 1/2 coverage with min 0.5")
	}
	if finals[0] != 0.9 {
		t.Errorf("unscored candidate final = %v, want its normalized relevance 0.9 untouched", finals[0])
	}
	if finals[1] >= finals[0] {
		t.Errorf("scored candidate (%v) must not outrank the unscored 0.9 — zero-coercion regression", finals[1])
	}
}

// Failure fallback carries no scores at all — coverage 0 ⇒ gate always skips.
func TestFallbackRerankIsUnscored(t *testing.T) {
	res := fallbackRerank([]RerankCandidate{{ID: "a", RetrievalRank: 1}, {ID: "b", RetrievalRank: 2}})
	for i, r := range res {
		if r.Scored {
			t.Errorf("fallback result %d claims Scored — it must not", i)
		}
	}
	if _, applied := BlendReranked([]float64{1, 0}, res, 0.5); applied {
		t.Error("blend applied on all-unscored fallback results")
	}
}
