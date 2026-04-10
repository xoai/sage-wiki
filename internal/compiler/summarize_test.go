package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/extract"
)

func TestGroupChunksNoGroupingNeeded(t *testing.T) {
	chunks := make([]extract.Chunk, 5)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// 2000 / 5 = 400 per chunk, above minChunkTokenBudget (200)
	groups := groupChunks(chunks, 2000)
	if len(groups) != 5 {
		t.Errorf("expected 5 groups (no grouping), got %d", len(groups))
	}
	for i, g := range groups {
		if len(g) != 1 {
			t.Errorf("group %d: expected 1 chunk, got %d", i, len(g))
		}
	}
}

func TestGroupChunksNeedsGrouping(t *testing.T) {
	chunks := make([]extract.Chunk, 60)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// 2000 / 60 = 33 per chunk, below minChunkTokenBudget (200)
	// maxGroups = 2000 / 200 = 10
	// chunksPerGroup = ceil(60 / 10) = 6
	groups := groupChunks(chunks, 2000)
	if len(groups) > 10 {
		t.Errorf("expected <= 10 groups, got %d", len(groups))
	}
	if len(groups) < 5 {
		t.Errorf("expected >= 5 groups, got %d", len(groups))
	}

	// All chunks accounted for
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total != 60 {
		t.Errorf("expected 60 total chunks across groups, got %d", total)
	}
}

func TestGroupChunksExtreme(t *testing.T) {
	chunks := make([]extract.Chunk, 200)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// 2000 / 200 = 10, way below minimum
	groups := groupChunks(chunks, 2000)
	// maxGroups = 10, chunksPerGroup = 20
	if len(groups) > 10 {
		t.Errorf("expected <= 10 groups, got %d", len(groups))
	}

	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total != 200 {
		t.Errorf("expected 200 total chunks, got %d", total)
	}
}

func TestGroupChunksSingleChunk(t *testing.T) {
	chunks := []extract.Chunk{{Index: 0, Text: "content"}}
	groups := groupChunks(chunks, 2000)
	if len(groups) != 1 || len(groups[0]) != 1 {
		t.Errorf("expected 1 group with 1 chunk, got %d groups", len(groups))
	}
}

func TestGroupChunksEmptyInput(t *testing.T) {
	groups := groupChunks(nil, 2000)
	if groups != nil {
		t.Errorf("expected nil for empty input, got %v", groups)
	}
}

func TestGroupChunksMaxTokensBelowMinBudget(t *testing.T) {
	chunks := make([]extract.Chunk, 10)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// maxTokens=100 < minChunkTokenBudget=200
	// maxGroups = 100/200 = 0, clamped to 1 → all chunks in one group
	groups := groupChunks(chunks, 100)
	if len(groups) != 1 {
		t.Errorf("expected 1 group when maxTokens < minBudget, got %d", len(groups))
	}
	if len(groups[0]) != 10 {
		t.Errorf("expected all 10 chunks in single group, got %d", len(groups[0]))
	}
}

func TestGroupChunksMaxTokensZero(t *testing.T) {
	chunks := make([]extract.Chunk, 5)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// maxTokens=0 → perChunkBudget=0, triggers grouping
	// maxGroups = 0/200 = 0, clamped to 1
	groups := groupChunks(chunks, 0)
	if len(groups) != 1 {
		t.Errorf("expected 1 group when maxTokens=0, got %d", len(groups))
	}
}

func TestSynthesizeHierarchicalEmpty(t *testing.T) {
	_, err := synthesizeHierarchical(nil, "test.md", nil, "", 2000)
	if err == nil {
		t.Error("expected error for empty summaries")
	}
}

func TestSynthesizeHierarchicalSingleSummary(t *testing.T) {
	// Single summary should pass through without LLM call
	result, err := synthesizeHierarchical([]string{"already done"}, "test.md", nil, "", 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "already done" {
		t.Errorf("expected pass-through, got %q", result)
	}
}
