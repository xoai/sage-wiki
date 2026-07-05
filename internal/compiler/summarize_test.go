package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/llm"
)

// TestEmptyContentError covers the shared guard used by all four compile
// content sites (single-chunk + image summarize, article writer, batch
// summarize): empty content → error carrying the EmptyContentDetails hint;
// non-empty → nil; nil response → error (defensive).
func TestEmptyContentError(t *testing.T) {
	err := emptyContentError(&llm.Response{FinishReason: "length", Reasoning: "thinking hard"}, "summary", "raw/x.md")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), `empty summary for "raw/x.md"`) {
		t.Errorf("error missing unit/name label: %v", err)
	}
	if !strings.Contains(err.Error(), "finish_reason=length") {
		t.Errorf("error missing EmptyContentDetails hint: %v", err)
	}

	if err := emptyContentError(&llm.Response{Content: "hello"}, "summary", "raw/x.md"); err != nil {
		t.Errorf("expected nil for non-empty content, got %v", err)
	}
	if err := emptyContentError(&llm.Response{Content: "   \n\t"}, "summary", "raw/x.md"); err == nil {
		t.Error("expected error for whitespace-only content")
	}
	if err := emptyContentError(nil, "batch summary", "raw/y.md"); err == nil {
		t.Error("expected error for nil response")
	}
}

// buildSummaryFile writes a fake summary file with the given frontmatter hash and body.
func buildSummaryFile(t *testing.T, dir, name, hash, body string) {
	t.Helper()
	summaryDir := filepath.Join(dir, "summaries")
	if err := os.MkdirAll(summaryDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nsource: %s\nsource_type: article\nsource_hash: %s\ncompiled_at: 2024-01-01\nchunk_count: 1\n---\n\n%s", name, hash, body)
	if err := os.WriteFile(filepath.Join(summaryDir, name+".md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSummarizeOneReusesExistingSummaryOnHashMatch(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"
	sourceHash := "sha256:abc123"
	longBody := strings.Repeat("x", 120)

	// SummaryFilename("sources/doc.md") is "sources-doc.md" with the
	// path-segment-joined naming (#51).
	buildSummaryFile(t, filepath.Join(projectDir, outputDir), "sources-doc", sourceHash, longBody)

	info := SourceInfo{Path: "sources/doc.md", Hash: sourceHash, Type: "article"}
	result := summarizeOne(projectDir, outputDir, info, nil, "", 0, nil, "", "", nil)

	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}
	if result.Summary != longBody {
		t.Errorf("expected reused summary body, got %q", result.Summary)
	}
	if result.SummaryPath != filepath.Join(outputDir, "summaries", "sources-doc.md") {
		t.Errorf("unexpected SummaryPath: %q", result.SummaryPath)
	}
}

func TestSummarizeOneSkipsReusedWhenHashMismatch(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"
	longBody := strings.Repeat("x", 120)

	buildSummaryFile(t, filepath.Join(projectDir, outputDir), "doc", "sha256:old", longBody)

	// Hash differs → must not reuse; extraction of missing source returns an error
	info := SourceInfo{Path: "sources/doc.md", Hash: "sha256:new", Type: "article"}
	result := summarizeOne(projectDir, outputDir, info, nil, "", 0, nil, "", "", nil)

	if result.Error == nil {
		t.Fatal("expected an error (extract should fail on missing source), got nil")
	}
	if result.Summary != "" {
		t.Errorf("expected no summary to be reused, got %q", result.Summary)
	}
}

func TestSummarizeOneSkipsReusedWhenSummaryTooShort(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"
	sourceHash := "sha256:abc123"

	buildSummaryFile(t, filepath.Join(projectDir, outputDir), "doc", sourceHash, "too short")

	// Body below 100 chars → must not reuse
	info := SourceInfo{Path: "sources/doc.md", Hash: sourceHash, Type: "article"}
	result := summarizeOne(projectDir, outputDir, info, nil, "", 0, nil, "", "", nil)

	if result.Error == nil {
		t.Fatal("expected an error (extract should fail on missing source), got nil")
	}
	if result.Summary != "" {
		t.Errorf("expected no summary to be reused, got %q", result.Summary)
	}
}

func TestSummarizeOneSkipsReusedWhenNoSummaryFile(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := "output"

	// No summary file exists → must not reuse
	info := SourceInfo{Path: "sources/doc.md", Hash: "sha256:abc123", Type: "article"}
	result := summarizeOne(projectDir, outputDir, info, nil, "", 0, nil, "", "", nil)

	if result.Error == nil {
		t.Fatal("expected an error (extract should fail on missing source), got nil")
	}
	if result.Summary != "" {
		t.Errorf("expected no summary to be reused, got %q", result.Summary)
	}
}

func TestGroupChunksNoGroupingNeeded(t *testing.T) {
	chunks := make([]extract.Chunk, 5)
	for i := range chunks {
		chunks[i] = extract.Chunk{Index: i, Text: "content"}
	}

	// 5000 / 5 = 1000 per chunk, >= minChunkTokenBudget (1000) → no grouping
	groups := groupChunks(chunks, 5000)
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

	// 2000 / 60 = 33 per chunk, below minChunkTokenBudget (1000)
	// maxGroups = 2000 / 1000 = 2
	// chunksPerGroup = ceil(60 / 2) = 30
	groups := groupChunks(chunks, 2000)
	if len(groups) > 4 {
		t.Errorf("expected <= 4 groups, got %d", len(groups))
	}
	if len(groups) < 2 {
		t.Errorf("expected >= 2 groups, got %d", len(groups))
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

	// 2000 / 200 = 10 per chunk, way below minChunkTokenBudget (1000)
	// maxGroups = 2000 / 1000 = 2, chunksPerGroup = 100
	groups := groupChunks(chunks, 2000)
	if len(groups) > 4 {
		t.Errorf("expected <= 4 groups, got %d", len(groups))
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

	// maxTokens=100 < minChunkTokenBudget=1000
	// maxGroups = 100/1000 = 0, clamped to 1 → all chunks in one group
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
	// maxGroups = 0/1000 = 0, clamped to 1
	groups := groupChunks(chunks, 0)
	if len(groups) != 1 {
		t.Errorf("expected 1 group when maxTokens=0, got %d", len(groups))
	}
}

func TestSynthesizeHierarchicalEmpty(t *testing.T) {
	_, err := synthesizeHierarchical(nil, "test.md", nil, "", 2000, "")
	if err == nil {
		t.Error("expected error for empty summaries")
	}
}

func TestSynthesizeHierarchicalSingleSummary(t *testing.T) {
	// Single summary should pass through without LLM call
	result, err := synthesizeHierarchical([]string{"already done"}, "test.md", nil, "", 2000, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "already done" {
		t.Errorf("expected pass-through, got %q", result)
	}
}

func TestValidateSummary(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantErr bool
	}{
		{"empty", "", true},
		{"too short", "This is short.", true},
		{"exactly 100 chars", string(make([]rune, 100)), false},
		{"valid summary", "这是一个足够长的摘要文本，包含了足够多的内容来通过最低质量检查。" +
			"我们需要确保摘要有足够的信息量，不能太短。这段文字需要超过一百个字符的最低要求才能通过验证。" +
			"所以我们在这里添加更多的内容来确保它足够长。", false},
		{"whitespace padded but short content", "   short   ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSummary(tt.text)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSummary(%q): got err=%v, wantErr=%v", tt.name, err, tt.wantErr)
			}
		})
	}
}
