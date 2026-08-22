package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
)

func emptyChoiceResponse() map[string]any {
	return map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": ""}, "finish_reason": "length"}},
		"model":   "m",
		"usage":   map[string]int{"total_tokens": 10},
	}
}

func conceptChoiceResponse() map[string]any {
	return map[string]any{
		"choices": []map[string]any{{"message": map[string]string{
			"content": `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`,
		}}},
		"model": "m",
		"usage": map[string]int{"total_tokens": 10},
	}
}

// TestExtractConcepts_AllEmptyReturnsError is the reproducing test: when every
// batch returns empty content (reasoning-model truncation), ExtractConcepts must
// return a non-nil error carrying the actionable hint — NOT silently report
// success with zero concepts.
func TestExtractConcepts_AllEmptyReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(emptyChoiceResponse())
	}))
	defer server.Close()

	client, err := llm.NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summaries := []SummaryResult{{SourcePath: "raw/a.md", Summary: "A valid summary of the source."}}

	concepts, err := ExtractConcepts(context.Background(), summaries, nil, client, "m", 20, 8192, 1, nil, nil)
	if err == nil {
		t.Fatal("expected error when all concept-extraction batches return empty content")
	}
	if len(concepts) != 0 {
		t.Errorf("expected 0 concepts, got %d", len(concepts))
	}
	for _, want := range []string{"finish_reason=length", "summary_max_tokens"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing actionable hint %q: %v", want, err)
		}
	}
}

// TestExtractConcepts_SuccessReturnsConcepts pins the happy path (unchanged).
func TestExtractConcepts_SuccessReturnsConcepts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(conceptChoiceResponse())
	}))
	defer server.Close()

	client, err := llm.NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summaries := []SummaryResult{{SourcePath: "raw/a.md", Summary: "A valid summary of the source."}}

	concepts, err := ExtractConcepts(context.Background(), summaries, nil, client, "m", 20, 8192, 1, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(concepts) != 1 || concepts[0].Name != "test-concept" {
		t.Errorf("expected 1 concept 'test-concept', got %+v", concepts)
	}
}

// TestExtractConcepts_ParseErrorReturnsError pins the parse-error failure exit:
// non-empty but unparseable content → every batch records a failure → total
// failure surfaces as an error (not a silent empty extraction).
func TestExtractConcepts_ParseErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "this is not json at all"}}},
			"model":   "m",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	client, err := llm.NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summaries := []SummaryResult{{SourcePath: "raw/a.md", Summary: "A valid summary."}}

	concepts, err := ExtractConcepts(context.Background(), summaries, nil, client, "m", 20, 8192, 1, nil, nil)
	if err == nil {
		t.Fatal("expected error when all batches return unparseable content")
	}
	if len(concepts) != 0 {
		t.Errorf("expected 0 concepts, got %d", len(concepts))
	}
}

// TestExtractConcepts_PartialFailureProceeds: one batch succeeds, one fails →
// the surviving concept is kept AND a *PartialFailureError names the skipped
// source (issue #166: partial failure must surface in result.Errors and name
// the skipped documents — the old nil-error contract reported errors=0 while
// half the corpus was silently skipped).
func TestExtractConcepts_PartialFailureProceeds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Fail by REQUEST CONTENT, not call order: with concurrency the batch
		// goroutines' sem acquisition order is not guaranteed, and the prompt
		// embeds the source path ("### Source: raw/b.md").
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "raw/b.md") {
			json.NewEncoder(w).Encode(emptyChoiceResponse())
		} else {
			json.NewEncoder(w).Encode(conceptChoiceResponse())
		}
	}))
	defer server.Close()

	client, err := llm.NewClient("openai", "fake-key", server.URL, -1)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summaries := []SummaryResult{
		{SourcePath: "raw/a.md", Summary: "First valid summary."},
		{SourcePath: "raw/b.md", Summary: "Second valid summary."},
	}

	// batchSize=1 → 2 batches; concurrency=1 → serialized so the call counter is
	// deterministic (exactly one batch succeeds, one fails).
	concepts, err := ExtractConcepts(context.Background(), summaries, nil, client, "m", 1, 8192, 1, nil, nil)
	if err == nil {
		t.Fatal("partial failure must return a non-nil *PartialFailureError (issue #166)")
	}
	var pfe *PartialFailureError
	if !errors.As(err, &pfe) {
		t.Fatalf("error type = %T, want *PartialFailureError (err=%v)", err, err)
	}
	if pfe.Failed != 1 || pfe.Total != 2 {
		t.Errorf("Failed/Total = %d/%d, want 1/2", pfe.Failed, pfe.Total)
	}
	if len(pfe.Skipped) != 1 || pfe.Skipped[0] != "raw/b.md" {
		t.Errorf("Skipped = %v, want [raw/b.md]", pfe.Skipped)
	}
	if len(concepts) != 1 {
		t.Errorf("expected 1 concept from the successful batch, got %d", len(concepts))
	}
}
