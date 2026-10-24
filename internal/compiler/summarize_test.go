package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false, "")

	if result.Error != nil {
		t.Fatalf("expected no error, got: %v", result.Error)
	}
	if result.Summary != longBody {
		t.Errorf("expected reused summary body, got %q", result.Summary)
	}
	if result.SummaryPath != filepath.ToSlash(filepath.Join(outputDir, "summaries", "sources-doc.md")) {
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
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false, "")

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
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false, "")

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
	result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false, "")

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
	_, err := synthesizeHierarchical(context.Background(), nil, "test.md", nil, "", 2000, "", nil)
	if err == nil {
		t.Error("expected error for empty summaries")
	}
}

func TestSynthesizeHierarchicalSingleSummary(t *testing.T) {
	// Single summary should pass through without LLM call
	result, err := synthesizeHierarchical(context.Background(), []string{"already done"}, "test.md", nil, "", 2000, "", nil)
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

// Issue #219 drift path: the reuse check was source-hash-only, so a
// template/models/config change scheduled docs via R5 drift and then the
// cache served the stale body on a PLAIN compile. The fix stamps a
// context_hash (tier-3 key minus source) into the frontmatter and requires
// it PRESENT AND MATCHING when the run computed one — absent ⇒ regenerate
// ⇒ restamp (self-healing per doc, closes the corpus-wide legacy hole).
func TestSummarizeReuseContextHash(t *testing.T) {
	longBody := strings.Repeat("fresh summary body ", 20) // ≥100 chars

	scenario := func(t *testing.T, name, storedCtx, runCtx string, wantReuse bool) {
		t.Helper()
		projectDir := t.TempDir()
		outputDir := "wiki"
		sourceHash := "sha256:src"
		info := SourceInfo{Path: "raw/doc.md", Hash: sourceHash, Type: "article"}

		// Fixture PRODUCED BY writeSummaryFile with the scenario's stamp —
		// the truest round-trip (the parser must read what the writer
		// emits; a hand-typed fixture would hide the field-order trap the
		// plan review flagged). storedCtx="" writes an empty-valued stamp;
		// the legacy scenario needs the LINE ABSENT, so strip it after.
		sc := &extract.SourceContent{Type: "article"}
		res := writeSummaryFile(projectDir, outputDir, info, "raw-doc.md", sc, longBody, SummaryResult{}, time.UTC, storedCtx)
		if res.Error != nil {
			t.Fatalf("fixture write: %v", res.Error)
		}
		if storedCtx == "" {
			// legacy shape: no context_hash line at all (pre-fix summaries)
			raw, _ := os.ReadFile(filepath.Join(projectDir, outputDir, "summaries", "raw-doc.md"))
			os.WriteFile(filepath.Join(projectDir, outputDir, "summaries", "raw-doc.md"),
				[]byte(strings.Replace(string(raw), "context_hash: \n", "", 1)), 0644)
		}

		// Source file must exist for the non-reuse regeneration path... no:
		// reuse path doesn't read the source. Only non-reuse needs it, and
		// it will fail on missing source — which still proves non-reuse.
		result := summarizeOne(context.Background(), projectDir, outputDir, info, nil, "", 0, nil, "", "", nil, nil, nil, false, runCtx)
		if wantReuse {
			if result.Summary != strings.TrimSpace(longBody) {
				t.Errorf("%s: want reuse, got regeneration or error (err=%v len=%d)",
					name, result.Error, len(result.Summary))
			}
		} else {
			if result.Summary == longBody && result.Error == nil {
				t.Errorf("%s: stale body reused — drift bug (issue #219)", name)
			}
		}
	}

	t.Run("hashed run, matching stamp → reuse", func(t *testing.T) {
		scenario(t, "match", "abc123", "abc123", true)
	})
	t.Run("hashed run, mismatched stamp → regenerate", func(t *testing.T) {
		scenario(t, "mismatch", "abc123", "def456", false)
	})
	t.Run("hashed run, absent stamp (legacy) → regenerate (self-heal)", func(t *testing.T) {
		scenario(t, "legacy", "", "abc123", false)
	})
	t.Run("unthreaded caller (empty run hash) → legacy reuse", func(t *testing.T) {
		scenario(t, "empty-run-hash", "abc123", "", true)
	})
}

// Config drift must also break reuse (the M3 closure): the context digest
// covers models + config, not just templates.
func TestSummarizeReuseBreaksOnConfigDrift(t *testing.T) {
	// Covered semantically by the mismatch scenario above (any context-hash
	// change → regenerate) — this test pins the DIGEST itself: same context,
	// same digest; any single component change, different digest.
	base := KeyContext{Templates: "t1", Models: "m1", Config: "c1", Embed: "e1"}
	d1 := base.ContextDigest()
	if d1 == "" {
		t.Fatal("ContextDigest empty")
	}
	if base.ContextDigest() != d1 {
		t.Fatal("ContextDigest not deterministic for identical context")
	}
	mutated := base
	mutated.Models = "m2"
	if mutated.ContextDigest() == d1 {
		t.Fatal("models change did not change ContextDigest — drift on models axis would reuse stale summary")
	}
	mutated = base
	mutated.Config = "c2"
	if mutated.ContextDigest() == d1 {
		t.Fatal("config change did not change ContextDigest — drift on config axis would reuse stale summary")
	}
	mutated = base
	mutated.Templates = "t2"
	if mutated.ContextDigest() == d1 {
		t.Fatal("template change did not change ContextDigest — drift on templates axis would reuse stale summary")
	}
}
