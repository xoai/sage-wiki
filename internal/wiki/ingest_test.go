package wiki

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// TestIngestConcurrentNoLostSource is the routing driver for D4: many ingests
// running at once must each land in the manifest. The old direct
// Load->AddSource->Save has no lock, so concurrent read-modify-write cycles
// clobber each other (last-writer-wins) and sources are lost. Routing IngestPath
// through manifest.Mutate serializes the RMW so every source survives.
func TestIngestConcurrentNoLostSource(t *testing.T) {
	dir := t.TempDir()
	if err := InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	srcDir := t.TempDir()
	const n = 10
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		p := filepath.Join(srcDir, fmt.Sprintf("article-%d.md", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("# Article %d\ncontent", i)), 0644); err != nil {
			t.Fatalf("write source %d: %v", i, err)
		}
		paths[i] = p
	}

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			if _, err := IngestPath(dir, p); err != nil {
				t.Errorf("IngestPath %s: %v", p, err)
			}
		}(p)
	}
	wg.Wait()

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if mf.SourceCount() != n {
		t.Errorf("lost update: expected %d sources, got %d", n, mf.SourceCount())
	}
}

func TestIngestLocalFile(t *testing.T) {
	dir := t.TempDir()
	InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Create a source file to ingest
	srcFile := filepath.Join(t.TempDir(), "article.md")
	os.WriteFile(srcFile, []byte("# Test Article\nSome content."), 0644)

	result, err := IngestPath(dir, srcFile)
	if err != nil {
		t.Fatalf("IngestPath: %v", err)
	}

	if result.Type != "article" {
		t.Errorf("expected article type, got %s", result.Type)
	}

	// Verify file was copied
	destPath := filepath.Join(dir, result.SourcePath)
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		t.Error("ingested file should exist at destination")
	}

	// Verify manifest updated
	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if mf.SourceCount() != 1 {
		t.Errorf("expected 1 source in manifest, got %d", mf.SourceCount())
	}
}

func TestIngestURL(t *testing.T) {
	dir := t.TempDir()
	InitGreenfield(dir, "test", "gemini-2.5-flash")

	// Disable SSRF check for test (mock server is on localhost)
	SkipSSRFCheck = true
	defer func() { SkipSSRFCheck = false }()

	// Mock web server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("# Web Article\n\nContent from the web."))
	}))
	defer server.Close()

	result, err := IngestURL(dir, server.URL+"/test-article")
	if err != nil {
		t.Fatalf("IngestURL: %v", err)
	}

	if result.Type != "article" {
		t.Errorf("expected article, got %s", result.Type)
	}

	// Verify file exists
	destPath := filepath.Join(dir, result.SourcePath)
	if _, err := os.Stat(destPath); os.IsNotExist(err) {
		t.Error("ingested URL should be saved as file")
	}
}

func TestSlugifyURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/article", "example-com-article"},
		{"http://blog.test/post/123", "blog-test-post-123"},
	}
	for _, tt := range tests {
		got := slugifyURL(tt.input)
		if got != tt.expected {
			t.Errorf("slugifyURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
