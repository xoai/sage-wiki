package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestEngine_TwoWorkspacesInterleaved is AC-B5: two Workspaces in one
// process with DIFFERENT prompt overrides compile concurrently; each run
// renders its own workspace's templates with no cross-contamination (the
// prompts instance isolation), and neither run perturbs the other's state.
func TestEngine_TwoWorkspacesInterleaved(t *testing.T) {
	build := func(marker string) (dir string, srv *httptest.Server, prompts *capture) {
		prompts = &capture{}
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if msgs, ok := body["messages"].([]any); ok {
				for _, m := range msgs {
					if mm, ok := m.(map[string]any); ok {
						// Capture only rendered user prompts: the sentinel lives
						// exclusively in the override template, so no
						// source-bearing or system message can satisfy the
						// override assertions (spec Test Integrity Constraints).
						if role, _ := mm["role"].(string); role != "user" {
							continue
						}
						if c, ok := mm["content"].(string); ok {
							prompts.add(c)
						}
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject matter.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source material."}, "finish_reason": "stop"}},
				"model":   "gpt-4o-mini",
				"usage":   map[string]int{"prompt_tokens": 50, "completion_tokens": 10, "total_tokens": 60},
			})
		}))
		t.Cleanup(srv.Close)

		dir = initWorkspace(t)
		extra := `version: 1
project: ws
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + srv.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  auto_commit: false
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(extra), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "prompts", "summarize-article.md"),
			[]byte(marker+" {{.SourcePath}}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "raw", "doc.md"), []byte("# Doc\n\nNeutral content for the test subject matter."), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, srv, prompts
	}

	dirA, _, promptsA := build("WORKSPACE-ALPHA")
	dirB, _, promptsB := build("WORKSPACE-BETA")

	wA, err := Open(context.Background(), dirA)
	if err != nil {
		t.Fatal(err)
	}
	defer wA.Close()
	wB, err := Open(context.Background(), dirB)
	if err != nil {
		t.Fatal(err)
	}
	defer wB.Close()

	// Interleave compile AND search concurrently (AC-B5: compile+search).
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for _, w := range []*Workspace{wA, wB} {
		wg.Add(2)
		go func(w *Workspace) {
			defer wg.Done()
			_, err := w.Compile(context.Background(), CompileRequest{Selector: "pending", Tier: 3})
			errs <- err
		}(w)
		go func(w *Workspace) {
			defer wg.Done()
			_, err := w.Search(context.Background(), SearchRequest{Query: "content", Limit: 3})
			errs <- err
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent compile+search: %v", err)
		}
	}

	if !promptsA.has("WORKSPACE-ALPHA") {
		t.Error("workspace A's compile never rendered its own override")
	}
	if promptsA.has("WORKSPACE-BETA") {
		t.Error("workspace A's compile rendered workspace B's template — cross-contamination")
	}
	if !promptsB.has("WORKSPACE-BETA") {
		t.Error("workspace B's compile never rendered its own override")
	}
	if promptsB.has("WORKSPACE-ALPHA") {
		t.Error("workspace B's compile rendered workspace A's template — cross-contamination")
	}
}

type capture struct {
	mu   sync.Mutex
	seen []string
}

func (c *capture) add(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, s)
}

func (c *capture) has(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.seen {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
