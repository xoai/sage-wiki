package search

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
)

func TestParseExpansionJSON_Valid(t *testing.T) {
	input := `{"lex":["keyword search","full text lookup"],"vec":["semantic meaning of query"],"hyde":"The answer would discuss..."}`
	eq, err := parseExpansionJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Lex) != 2 {
		t.Errorf("expected 2 lex variants, got %d", len(eq.Lex))
	}
	if len(eq.Vec) != 1 {
		t.Errorf("expected 1 vec variant, got %d", len(eq.Vec))
	}
	if eq.Hyde == "" {
		t.Error("expected non-empty hyde")
	}
}

func TestParseExpansionJSON_CodeFenced(t *testing.T) {
	input := "```json\n{\"lex\":[\"alpha\",\"beta\"],\"vec\":[\"gamma\"],\"hyde\":\"delta\"}\n```"
	eq, err := parseExpansionJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Lex) != 2 || eq.Lex[0] != "alpha" {
		t.Errorf("unexpected lex: %v", eq.Lex)
	}
}

func TestParseExpansionJSON_WithPreamble(t *testing.T) {
	input := "Here are the search variants:\n{\"lex\":[\"x\",\"y\"],\"vec\":[\"z\"],\"hyde\":\"h\"}"
	eq, err := parseExpansionJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Lex) != 2 {
		t.Errorf("expected 2 lex variants, got %d", len(eq.Lex))
	}
}

func TestParseExpansionJSON_Malformed(t *testing.T) {
	_, err := parseExpansionJSON("this is not json at all")
	if err == nil {
		t.Error("expected error for malformed input")
	}
}

func TestParseExpansionJSON_Empty(t *testing.T) {
	_, err := parseExpansionJSON("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseExpansionJSON_MissingFields(t *testing.T) {
	// Missing hyde — should still parse, hyde is empty string
	input := `{"lex":["a","b"],"vec":["c"]}`
	eq, err := parseExpansionJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Lex) != 2 {
		t.Errorf("expected 2 lex, got %d", len(eq.Lex))
	}
	if eq.Hyde != "" {
		t.Errorf("expected empty hyde, got %q", eq.Hyde)
	}
}

func TestExpandedQuery_AllQueries(t *testing.T) {
	eq := &ExpandedQuery{
		Original: "how does chunking work",
		Lex:      []string{"chunking algorithm", "text splitting"},
	}
	all := eq.AllQueries()
	if len(all) != 3 {
		t.Errorf("expected 3 queries, got %d", len(all))
	}
	if all[0] != "how does chunking work" {
		t.Errorf("first should be original, got %q", all[0])
	}
}

func TestFallbackExpansion(t *testing.T) {
	eq := fallbackExpansion("test query")
	if eq.Original != "test query" {
		t.Error("fallback should preserve original")
	}
	if len(eq.Lex) != 0 || len(eq.Vec) != 0 || eq.Hyde != "" {
		t.Error("fallback should have no variants")
	}
}

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStrongSignal_HighScoreBigGap(t *testing.T) {
	db := openTestDB(t)
	ms := memory.NewStore(db)

	// Add many entries so BM25 IDF gives higher discrimination
	ms.Add(memory.Entry{ID: "1", Content: "goroutines goroutines goroutines concurrent execution goroutines channels"})
	ms.Add(memory.Entry{ID: "2", Content: "python flask web framework for building APIs and servers"})
	ms.Add(memory.Entry{ID: "3", Content: "javascript react components rendering virtual dom"})
	ms.Add(memory.Entry{ID: "4", Content: "database sql queries indexing optimization performance"})
	ms.Add(memory.Entry{ID: "5", Content: "network tcp udp protocol socket programming"})

	if !StrongSignal("goroutines concurrent", ms) {
		t.Error("expected strong signal for highly specific query")
	}
}

func TestStrongSignal_LowScore(t *testing.T) {
	db := openTestDB(t)
	ms := memory.NewStore(db)

	ms.Add(memory.Entry{ID: "1", Content: "alpha beta gamma delta epsilon"})

	// Query with weak match
	if StrongSignal("zeta theta", ms) {
		t.Error("expected no strong signal for weak match")
	}
}

func TestStrongSignal_EmptyResults(t *testing.T) {
	db := openTestDB(t)
	ms := memory.NewStore(db)

	if StrongSignal("anything", ms) {
		t.Error("expected no strong signal for empty index")
	}
}

func TestNormalizeBM25(t *testing.T) {
	tests := []struct {
		score    float64
		expected float64
	}{
		{0, 0},
		{1, 0.5},
		{4, 0.8},
		{-2, 2.0 / 3.0},
	}
	for _, tt := range tests {
		got := normalizeBM25(tt.score)
		if math.Abs(got-tt.expected) > 0.01 {
			t.Errorf("normalizeBM25(%f) = %f, want %f", tt.score, got, tt.expected)
		}
	}
}
