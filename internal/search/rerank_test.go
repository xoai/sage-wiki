package search

import (
	"math"
	"testing"
)

func TestParseRerankJSON_Valid(t *testing.T) {
	input := `[{"id":1,"score":8},{"id":2,"score":3},{"id":3,"score":6}]`
	scores, err := parseRerankJSON(input, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 3 {
		t.Fatalf("expected 3 scores, got %d", len(scores))
	}
	if scores[0] != 8 || scores[1] != 3 || scores[2] != 6 {
		t.Errorf("unexpected scores: %v", scores)
	}
}

func TestParseRerankJSON_CodeFenced(t *testing.T) {
	input := "```json\n[{\"id\":1,\"score\":9},{\"id\":2,\"score\":1}]\n```"
	scores, err := parseRerankJSON(input, 2)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0] != 9 || scores[1] != 1 {
		t.Errorf("unexpected scores: %v", scores)
	}
}

func TestParseRerankJSON_Malformed(t *testing.T) {
	_, err := parseRerankJSON("not json", 3)
	if err == nil {
		t.Error("expected error for malformed input")
	}
}

func TestParseRerankJSON_OutOfRange(t *testing.T) {
	// ID 5 is out of range for 3 candidates — should be ignored
	input := `[{"id":1,"score":8},{"id":5,"score":3}]`
	scores, err := parseRerankJSON(input, 3)
	if err != nil {
		t.Fatal(err)
	}
	if scores[0] != 8 {
		t.Errorf("expected score 8 for id 1, got %f", scores[0])
	}
	if scores[1] != 0 || scores[2] != 0 {
		t.Errorf("out-of-range entries should not affect scores: %v", scores)
	}
}

func TestFallbackRerank(t *testing.T) {
	candidates := []RerankCandidate{
		{ID: "a", RetrievalRank: 1},
		{ID: "b", RetrievalRank: 2},
	}
	results := fallbackRerank(candidates)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "a" || results[1].ID != "b" {
		t.Error("fallback should preserve order")
	}
	if results[0].Score != 0 || results[1].Score != 0 {
		t.Error("fallback scores should be 0")
	}
}

func TestTruncateToTokens(t *testing.T) {
	long := "word " // ~1.25 tokens
	for i := 0; i < 500; i++ {
		long += "word "
	}
	truncated := truncateToTokens(long, 100)
	tokens := estimateTokensLen(truncated)
	if tokens > 120 { // allow some slack due to estimation
		t.Errorf("truncated text too long: ~%d tokens", tokens)
	}
}

func estimateTokensLen(s string) int {
	return len(s) / 4
}

func TestBlendScores(t *testing.T) {
	// Verify the math for position-aware blending
	tests := []struct {
		rrfScore      float64
		rerankScore   float64
		retrievalRank int
		expected      float64
	}{
		{1.0, 0.5, 1, 0.75*1.0 + 0.25*0.5},   // rank 1-3: 75/25
		{1.0, 0.5, 3, 0.75*1.0 + 0.25*0.5},   // rank 3: still 75/25
		{1.0, 0.5, 5, 0.60*1.0 + 0.40*0.5},   // rank 4-10: 60/40
		{1.0, 0.5, 10, 0.60*1.0 + 0.40*0.5},  // rank 10: still 60/40
		{1.0, 0.5, 11, 0.40*1.0 + 0.60*0.5},  // rank 11+: 40/60
	}
	for _, tt := range tests {
		got := BlendScore(tt.rrfScore, tt.rerankScore, tt.retrievalRank)
		if math.Abs(got-tt.expected) > 0.001 {
			t.Errorf("BlendScore(%f, %f, rank=%d) = %f, want %f",
				tt.rrfScore, tt.rerankScore, tt.retrievalRank, got, tt.expected)
		}
	}
}
