package compiler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// tempCapture records the temperature field presence/value of every chat
// completion request, mutex-guarded (the compiler fires concurrent calls —
// same data-race family as the httptest capture learning).
type tempCapture struct {
	mu       sync.Mutex
	present  []bool
	values   []float64
	requests int
}

func (c *tempCapture) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		if body["input"] != nil { // embeddings request
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0}},
			})
			return
		}

		c.mu.Lock()
		c.requests++
		if v, ok := body["temperature"]; ok {
			c.present = append(c.present, true)
			if f, ok := v.(float64); ok {
				c.values = append(c.values, f)
			}
		} else {
			c.present = append(c.present, false)
		}
		c.mu.Unlock()

		messages, _ := body["messages"].([]any)
		var content string
		isExtract := false
		for _, m := range messages {
			if mm, ok := m.(map[string]any); ok {
				if c, _ := mm["content"].(string); strings.Contains(c, "concept extraction system") {
					isExtract = true
				}
			}
		}
		messages2, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages2) > 0 {
			if m, ok := messages2[len(messages2)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}
		switch {
		case isExtract:
			content = `[{"name": "temp-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			content = "# Temp Concept\n\nTemperature wire test article body with enough content to pass validation.\n\n## See also\n\n[[temp-concept]]"
		default:
			content = "## Key claims\n\nTemperature wire test body with sufficient length to pass the summary quality gate checks.\n\n## Concepts\n\ntemp-concept: A concept for the temperature test."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 50},
		})
	}
}

func compileAgainstTempStub(t *testing.T, extraConfig string, cap *tempCapture) {
	t.Helper()
	server := httptest.NewServer(cap.handler(t))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "temptest", "gpt-4o-mini")
	cfg := fmt.Sprintf(`
version: 1
project: temptest
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: %s
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
%s
`, server.URL, extraConfig)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Temp\n\nTemperature determinism content."), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// TestCompile_SendsTemperatureZero pins SPEC-04 D2: every compile chat
// request carries an explicit temperature: 0 on the wire by default.
func TestCompile_SendsTemperatureZero(t *testing.T) {
	cap := &tempCapture{}
	compileAgainstTempStub(t, "", cap)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.requests == 0 {
		t.Fatal("no chat requests captured")
	}
	for i, p := range cap.present {
		if !p {
			t.Errorf("request %d: temperature field ABSENT, want explicit 0", i)
		}
	}
	for i, v := range cap.values {
		if v != 0.0 {
			t.Errorf("request %d: temperature = %v, want 0", i, v)
		}
	}
	t.Logf("captured %d chat requests, all temperature:0", cap.requests)
}

// TestCompile_TemperatureOverrideLands pins the compiler.temperature config.
func TestCompile_TemperatureOverrideLands(t *testing.T) {
	cap := &tempCapture{}
	compileAgainstTempStub(t, "  temperature: 0.7\n", cap)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.requests == 0 {
		t.Fatal("no chat requests captured")
	}
	for i, v := range cap.values {
		if v != 0.7 {
			t.Errorf("request %d: temperature = %v, want 0.7", i, v)
		}
	}
}
