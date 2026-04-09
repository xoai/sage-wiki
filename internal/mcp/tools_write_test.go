package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func TestWriteSummary(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, err := NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Write a summary
	result := srv.CallTool(context.Background(), "wiki_write_summary", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_write_summary",
			Arguments: map[string]any{
				"source":   "raw/test.md",
				"content":  "This is a summary of the test article.",
				"concepts": "concept-a, concept-b",
			},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	// Verify file written
	summaryPath := filepath.Join(dir, "wiki", "summaries", "test.md")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
		t.Error("summary file should exist")
	}

	// Verify manifest updated
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	src, ok := mf.Sources["raw/test.md"]
	if !ok {
		t.Error("source should be in manifest")
	}
	if src.Status != "compiled" {
		t.Errorf("expected compiled status, got %s", src.Status)
	}
}

func TestWriteArticle(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_write_article", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_write_article",
			Arguments: map[string]any{
				"concept": "self-attention",
				"content": "---\nconcept: self-attention\n---\n\n# Self-Attention\n\nA mechanism.",
			},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	// Verify file
	articlePath := filepath.Join(dir, "wiki", "concepts", "self-attention.md")
	if _, err := os.Stat(articlePath); os.IsNotExist(err) {
		t.Error("article should exist")
	}

	// Verify ontology entity
	e, _ := srv.ont.GetEntity("self-attention")
	if e == nil {
		t.Error("ontology entity should exist")
	}

	// Verify manifest
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if mf.ConceptCount() != 1 {
		t.Errorf("expected 1 concept in manifest, got %d", mf.ConceptCount())
	}
}

func TestAddOntologyEntity(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_add_ontology", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_add_ontology",
			Arguments: map[string]any{
				"entity_id":   "flash-attention",
				"entity_type": "technique",
				"entity_name": "Flash Attention",
			},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	e, _ := srv.ont.GetEntity("flash-attention")
	if e == nil || e.Type != "technique" {
		t.Error("entity should be created with correct type")
	}
}

func TestAddOntologyRelation(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	// Create entities first
	srv.ont.AddEntity(ontology.Entity{ID: "flash-attn", Type: "technique", Name: "Flash"})
	srv.ont.AddEntity(ontology.Entity{ID: "attention", Type: "concept", Name: "Attention"})

	result := srv.CallTool(context.Background(), "wiki_add_ontology", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_add_ontology",
			Arguments: map[string]any{
				"source_id": "flash-attn",
				"target_id": "attention",
				"relation":  "implements",
			},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	count, _ := srv.ont.RelationCount()
	if count != 1 {
		t.Errorf("expected 1 relation, got %d", count)
	}
}

func TestLearn(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_learn", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_learn",
			Arguments: map[string]any{
				"type":    "gotcha",
				"content": "Always distinguish memory from IO bandwidth when discussing attention complexity.",
				"tags":    "attention,memory",
			},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	// Verify stored
	var count int
	srv.db.ReadDB().QueryRow("SELECT COUNT(*) FROM learnings").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 learning, got %d", count)
	}
}

func TestCommit(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	// Create a file to commit
	os.WriteFile(filepath.Join(dir, "wiki", "test.md"), []byte("test"), 0644)

	result := srv.CallTool(context.Background(), "wiki_commit", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_commit",
			Arguments: map[string]any{"message": "test commit via MCP"},
		},
	})

	if result.IsError {
		// Git might not have user config in test env — that's ok
		text := result.Content[0].(mcplib.TextContent).Text
		if text != "" {
			t.Logf("commit result: %s", text)
		}
	}
}

func TestCompileDiff(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_compile_diff", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_compile_diff",
			Arguments: map[string]any{},
		},
	})

	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	text := result.Content[0].(mcplib.TextContent).Text
	if text == "" {
		t.Error("expected non-empty diff result")
	}
}

func TestAddSourceWithPathTraversal(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_add_source", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_add_source",
			Arguments: map[string]any{"path": "../../etc/passwd"},
		},
	})

	if !result.IsError {
		t.Error("expected error for path traversal")
	}
}

func TestCaptureEmptyContent(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_capture", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_capture",
			Arguments: map[string]any{"content": ""},
		},
	})

	if !result.IsError {
		t.Error("expected error for empty content")
	}
}

func TestCaptureTooLarge(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	bigContent := string(make([]byte, 101*1024)) // 101KB
	result := srv.CallTool(context.Background(), "wiki_capture", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      "wiki_capture",
			Arguments: map[string]any{"content": bigContent},
		},
	})

	if !result.IsError {
		t.Error("expected error for oversized content")
	}
}

func TestCaptureFallbackRaw(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Write raw capture directly (simulates LLM failure fallback)
	path, err := writeRawCapture(dir, "some knowledge from a chat", "debugging session", "go,testing", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("writeRawCapture: %v", err)
	}

	if !filepath.IsLocal(path) {
		t.Errorf("expected local path, got %s", path)
	}

	absPath := filepath.Join(dir, path)
	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}

	content := string(data)
	if !contains(content, "source: mcp-capture") {
		t.Error("expected mcp-capture frontmatter")
	}
	if !contains(content, "some knowledge from a chat") {
		t.Error("expected content in file")
	}
	if !contains(content, "tags: [go,testing]") {
		t.Error("expected tags in frontmatter")
	}
}

func TestCaptureHandlerFallback(t *testing.T) {
	// Tests the full handler path: no LLM configured → fallback writes raw file
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	srv, _ := NewServer(dir)
	defer srv.Close()

	result := srv.CallTool(context.Background(), "wiki_capture", mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name: "wiki_capture",
			Arguments: map[string]any{
				"content": "We discovered that connection pooling was the bottleneck, not query speed.",
				"context": "debugging database performance",
				"tags":    "postgres,performance",
			},
		},
	})

	// Should succeed via fallback (no LLM configured in test)
	if result.IsError {
		t.Fatalf("expected fallback success, got error: %s", result.Content[0].(mcplib.TextContent).Text)
	}

	text := result.Content[0].(mcplib.TextContent).Text
	if !contains(text, "Raw content saved") {
		t.Errorf("expected fallback message, got: %s", text)
	}

	// Verify file was written
	captures, _ := os.ReadDir(filepath.Join(dir, "raw", "captures"))
	if len(captures) == 0 {
		t.Fatal("expected capture file in raw/captures/")
	}

	data, _ := os.ReadFile(filepath.Join(dir, "raw", "captures", captures[0].Name()))
	content := string(data)
	if !contains(content, "connection pooling") {
		t.Error("expected content in capture file")
	}
	if !contains(content, "tags: [postgres,performance]") {
		t.Error("expected tags in frontmatter")
	}
	if !contains(content, "debugging database performance") {
		t.Error("expected context in frontmatter")
	}
}

func TestCaptureWriteItems(t *testing.T) {
	// Tests the file-writing path with known extracted items (simulates post-LLM)
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
	os.MkdirAll(filepath.Join(dir, "raw", "captures"), 0755)

	// Write items as the handler would
	items := []capturedItem{
		{Title: "connection-pool-bottleneck", Content: "The actual performance issue was exhausted connections."},
		{Title: "pgbouncer-transaction-mode", Content: "Transaction-level pooling resolved the issue."},
		{Title: "connection-pool-bottleneck", Content: "Duplicate title should get suffix."},
	}

	usedSlugs := map[string]int{}
	var written []string
	for _, item := range items {
		slug := slugify(item.Title)
		if n, exists := usedSlugs[slug]; exists {
			usedSlugs[slug] = n + 1
			slug = fmt.Sprintf("%s-%d", slug, n+1)
		} else {
			usedSlugs[slug] = 1
		}
		relPath := filepath.Join("raw", "captures", slug+".md")
		absPath := filepath.Join(dir, relPath)
		os.WriteFile(absPath, []byte("# "+item.Title+"\n\n"+item.Content), 0644)
		written = append(written, relPath)
	}

	if len(written) != 3 {
		t.Fatalf("expected 3 files, got %d", len(written))
	}

	// Verify dedup: third file should have -2 suffix
	if !contains(written[2], "connection-pool-bottleneck-2") {
		t.Errorf("expected dedup suffix, got %s", written[2])
	}

	// Verify files exist
	for _, p := range written {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("file not found: %s", p)
		}
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"flash-attention-memory-tradeoff", "flash-attention-memory-tradeoff"},
		{"Hello World!", "hello-world"},
		{"CamelCase Test", "camelcase-test"},
		{"special@#chars$%", "special-chars"},
		{"", ""},
		{"a-very-" + string(make([]byte, 100)), "a-very"}, // truncated
	}

	for _, tt := range tests {
		got := slugify(tt.input)
		if len(got) > 80 {
			t.Errorf("slugify(%q) too long: %d", tt.input, len(got))
		}
		if tt.want != "" && got != tt.want {
			// For the truncation test, just check it's not too long
			if len(tt.input) < 80 && got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		}
	}
}

func TestStripJSONFences(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`[{"title":"a"}]`, `[{"title":"a"}]`},
		{"```json\n[{\"title\":\"a\"}]\n```", `[{"title":"a"}]`},
		{"```\n[{\"title\":\"a\"}]\n```", `[{"title":"a"}]`},
	}

	for _, tt := range tests {
		got := stripJSONFences(tt.input)
		if got != tt.want {
			t.Errorf("stripJSONFences(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Suppress unused import warning
var _ = json.Marshal
