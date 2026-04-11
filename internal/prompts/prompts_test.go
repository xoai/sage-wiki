package prompts

import (
	"strings"
	"testing"
)

func TestAvailable(t *testing.T) {
	names := Available()
	if len(names) == 0 {
		t.Fatal("no templates loaded")
	}

	expected := []string{"summarize_article.txt", "summarize_paper.txt", "extract_concepts.txt", "write_article.txt", "caption_image.txt"}
	for _, exp := range expected {
		found := false
		for _, name := range names {
			if name == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing template %q", exp)
		}
	}
}

func TestRenderSummarize(t *testing.T) {
	result, err := Render("summarize_article", SummarizeData{
		SourcePath: "raw/articles/test.md",
		SourceType: "article",
		MaxTokens:  2000,
	}, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(result, "raw/articles/test.md") {
		t.Error("expected source path in output")
	}
	if !strings.Contains(result, "2000") {
		t.Error("expected max tokens in output")
	}
	if !strings.Contains(result, "## Key claims") {
		t.Error("expected Key claims section")
	}
}

func TestRenderWriteArticle(t *testing.T) {
	result, err := Render("write_article", WriteArticleData{
		ConceptName:     "Self-Attention",
		ConceptID:       "self-attention",
		Sources:         "attention-paper, transformer-explainer",
		RelatedConcepts: []string{"multi-head-attention", "positional-encoding"},
		MaxTokens:       4000,
		Confidence:      "high",
	}, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(result, "Self-Attention") {
		t.Error("expected concept name")
	}
	if !strings.Contains(result, "[[multi-head-attention]]") {
		t.Error("expected wikilinks in See also")
	}
}

func TestRenderCaption(t *testing.T) {
	result, err := Render("caption_image", CaptionData{
		SourcePath: "raw/papers/figure1.png",
	}, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(result, "raw/papers/figure1.png") {
		t.Error("expected source path")
	}
}

func TestRenderLanguageInjection(t *testing.T) {
	// Language instruction should be appended for non-JSON templates
	result, err := Render("summarize_article", SummarizeData{
		SourcePath: "raw/test.md",
		SourceType: "article",
		MaxTokens:  1000,
	}, "Chinese")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(result, "Write your entire response in Chinese") {
		t.Error("expected language instruction for non-JSON template")
	}
}

func TestRenderLanguageSkippedForJSON(t *testing.T) {
	// Language instruction should NOT be appended for JSON-output templates
	result, err := Render("extract_concepts", ExtractData{
		ExistingConcepts: "attention",
		Summaries:        "test summary",
	}, "Chinese")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(result, "Write your entire response in") {
		t.Error("language instruction should be skipped for JSON-output templates")
	}
}

func TestRenderLanguageSkippedForCapture(t *testing.T) {
	// capture_knowledge also requires JSON output
	result, err := Render("capture_knowledge", CaptureData{
		Context: "test",
	}, "Chinese")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(result, "Write your entire response in") {
		t.Error("language instruction should be skipped for JSON-output templates")
	}
}

func TestRenderNoLanguage(t *testing.T) {
	// No language instruction when language is empty
	result, err := Render("summarize_article", SummarizeData{
		SourcePath: "raw/test.md",
		SourceType: "article",
		MaxTokens:  1000,
	}, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(result, "Write your entire response in") {
		t.Error("should not inject language when language is empty")
	}
}

func TestIsJSONTemplate(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"Output ONLY a JSON array of objects.", true},
		{"Return ONLY a JSON array.", true},
		{"output only a json array", true},
		{"Write a summary.", false},
		{"Return results in markdown format.", false},
	}
	for _, tt := range tests {
		got := isJSONTemplate(tt.content)
		if got != tt.want {
			t.Errorf("isJSONTemplate(%q) = %v, want %v", tt.content[:30], got, tt.want)
		}
	}
}
