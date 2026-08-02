package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestSpikeDoubleCompile is the SPEC-04 Task-1 spike: compile the same 3-doc
// corpus into two temp dirs with a deterministic stub provider and pinned
// SOURCE_DATE_EPOCH, then byte-compare every file pair. It prints the drift
// inventory (classified by the D-rule expected to fix it) instead of failing
// on drift — this is the go/no-go measurement for AC-1's byte-parity claim.
// Task 13 absorbs this harness into the real double-compile AC test, which
// DOES fail on drift outside the documented exclusion list.
func TestSpikeDoubleCompile(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	server := spikeStubProvider(t)
	defer server.Close()

	dirA := spikeCompileOnce(t, server.URL)
	dirB := spikeCompileOnce(t, server.URL)

	drift := spikeDiffTrees(t, dirA, dirB)
	sort.Strings(drift)

	t.Logf("SPIKE drift inventory (%d differing paths):", len(drift))
	for _, d := range drift {
		t.Logf("  DRIFT %s", d)
	}
	if len(drift) == 0 {
		t.Log("SPIKE: trees already byte-identical — AC-1 needs no fallback")
	}
}

// spikeStubProvider returns deterministic OpenAI-shaped responses: summarize,
// extract, write, and embeddings. Responses depend only on request content,
// never on time or call order.
func spikeStubProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			input, _ := body["input"].(string)
			vec := make([]float64, 8)
			for i := range vec {
				vec[i] = float64(len(input)+i) / 100.0
			}
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": vec, "index": 0}},
			})
			return
		}

		messages, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}

		var content string
		switch {
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name": "spike-concept", "aliases": ["spike alias"], "sources": ["raw/doc1.md", "raw/doc2.md"], "type": "concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			content = "# Spike Concept\n\nA deterministic spike article body with enough content to pass validation checks.\n\n## See also\n\n[[spike-concept]]"
		default:
			content = "## Key claims\n\nThis spike document discusses deterministic compilation and byte-identical artifacts in sufficient detail to pass the quality gate.\n\n## Concepts\n\nspike-concept: A concept used by the determinism spike."
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 100},
		})
	}))
}

func spikeCompileOnce(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "spike", "gpt-4o-mini")

	cfg := fmt.Sprintf(`
version: 1
project: spike
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
  max_parallel: 4
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`, serverURL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	for i, name := range []string{"doc1.md", "doc2.md", "doc3.md"} {
		body := fmt.Sprintf("# Spike Doc %d\n\nDeterministic spike content %d about compilation and artifacts.", i+1, i+1)
		if err := os.WriteFile(filepath.Join(dir, "raw", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("spike Compile: %v", err)
	}
	if keep := os.Getenv("SAGE_SPIKE_KEEP"); keep != "" {
		dst := filepath.Join(keep, filepath.Base(dir))
		if err := os.MkdirAll(dst, 0755); err == nil {
			for _, f := range []string{".sage/wiki.db", ".manifest.json", ".sage/usage.jsonl"} {
				b, rerr := os.ReadFile(filepath.Join(dir, f))
				if rerr == nil {
					os.MkdirAll(filepath.Join(dst, filepath.Dir(f)), 0755)
					os.WriteFile(filepath.Join(dst, f), b, 0644)
				}
			}
		}
	}
	return dir
}

// spikeDiffTrees walks both trees and returns one line per path whose bytes
// differ (or that exists on only one side), relative to the tree root.
func spikeDiffTrees(t *testing.T, dirA, dirB string) []string {
	t.Helper()
	filesA := spikeSnapshot(t, dirA)
	filesB := spikeSnapshot(t, dirB)

	var drift []string
	for rel, bytesA := range filesA {
		bytesB, ok := filesB[rel]
		if !ok {
			drift = append(drift, rel+" (only in A)")
			continue
		}
		if !bytes.Equal(bytesA, bytesB) {
			drift = append(drift, fmt.Sprintf("%s (%d vs %d bytes)", rel, len(bytesA), len(bytesB)))
		}
	}
	for rel := range filesB {
		if _, ok := filesA[rel]; !ok {
			drift = append(drift, rel+" (only in B)")
		}
	}
	return drift
}

func spikeSnapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}
