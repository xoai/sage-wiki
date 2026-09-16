package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// SPEC-08 Task 13: max_compile_batch fail-fast + the provider-concurrency
// ceiling.

func TestCompileBatchCapFailsFast(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "batchcap", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Three sources; cap at 2 → the run must fail fast before any doc is
	// compiled (nothing partial persists).
	for i, name := range []string{"one.md", "two.md", "three.md"} {
		p := filepath.Join(dir, "raw", name)
		if err := os.WriteFile(p, []byte("# Doc\ncontent "+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_compile_batch: 2\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = Compile(dir, CompileOpts{})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if le.Which != limits.WhichCompileBatch {
		t.Errorf("Which = %q, want %q", le.Which, limits.WhichCompileBatch)
	}
	if le.Limit != 2 || le.Got != 3 {
		t.Errorf("Limit/Got = %d/%d, want 2/3", le.Limit, le.Got)
	}
	// Nothing partial persists: no summaries written.
	entries, _ := os.ReadDir(filepath.Join(dir, "wiki", "summaries"))
	if len(entries) != 0 {
		t.Errorf("over-batch compile persisted %d summaries", len(entries))
	}
}

func TestCompileUnderBatchCapProceeds(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "batchok", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "one.md"), []byte("# Doc\ncontent"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_compile_batch: 5\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// Under the cap the batch guard must not fire; the run proceeds until
	// the (unconfigured-here) LLM stage — any error must NOT be the
	// compile_batch limit.
	_, err = Compile(dir, CompileOpts{})
	if errors.As(err, new(*limits.LimitError)) && strings.Contains(err.Error(), limits.WhichCompileBatch) {
		t.Fatalf("under-cap run hit the batch guard: %v", err)
	}
}

func TestProviderConcurrencyCeiling(t *testing.T) {
	cfg := config.Defaults()
	cfg.Compiler.MaxParallel = 50
	cfg.Limits.MaxConcurrentProviderCalls = 20
	if got := providerConcurrency(&cfg); got != 20 {
		t.Errorf("providerConcurrency = %d, want 20 (limits ceiling wins)", got)
	}
	cfg.Limits.MaxConcurrentProviderCalls = 100
	if got := providerConcurrency(&cfg); got != 50 {
		t.Errorf("providerConcurrency = %d, want 50 (compiler.max_parallel lower)", got)
	}
}

// Issue #186 harness: tier-1 workspace against an openai-compatible mock
// serving /embeddings (tier 1 = index + embed; no chat calls on this path).
func tier1Harness(t *testing.T, nDocs int) (dir string, setLimit func(int)) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}}},
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "unused on tier 1"}}},
			"model":   "m",
		})
	}))
	t.Cleanup(server.Close)

	dir = t.TempDir()
	if err := wiki.InitGreenfield(dir, "t186", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	setLimit = func(n int) {
		cfg := `version: 1
project: t186
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + server.URL + `
embed:
  provider: openai
  model: text-embedding-3-small
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 1
limits:
  max_compile_batch: ` + fmt.Sprint(n) + `
`
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < nDocs; i++ {
		if err := os.WriteFile(filepath.Join(dir, "raw", fmt.Sprintf("d%d.md", i)),
			[]byte(fmt.Sprintf("# Doc %d\n\ncontent %d", i, i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, setLimit
}

// Issue #186 repro B: a fully-compiled tier<3 corpus must reach the
// nothing-to-compile fast path even when the corpus exceeds
// max_compile_batch — the limit bounds per-run WORK, not corpus size.
// Pre-fix: every compile aborts with compile_batch (raw diff counted
// before skip classification prunes the already-compiled docs).
func TestCompileBatchCap_CompiledCorpusPasses(t *testing.T) {
	dir, setLimit := tier1Harness(t, 3)
	setLimit(5)
	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("initial compile (limit above corpus): %v", err)
	}

	// Lower the limit below the (now fully compiled) corpus: must succeed —
	// classification prunes every doc, work = 0.
	setLimit(2)
	if _, err := Compile(dir, CompileOpts{}); err != nil {
		var le *limits.LimitError
		if errors.As(err, &le) {
			t.Fatalf("fully-compiled corpus tripped %s (limit %d, got %d) — raw diff counted before classification (issue #186)", le.Which, le.Limit, le.Got)
		}
		t.Fatalf("second compile: %v", err)
	}
}

// Issue #186 MaxDocs drain: pending work above the limit, but MaxDocs
// truncates the run below it — the limit must compare against the work the
// run will actually process, not the pending backlog.
func TestCompileBatchCap_MaxDocsCanDrain(t *testing.T) {
	dir, setLimit := tier1Harness(t, 3)
	setLimit(2) // 3 pending > 2, but MaxDocs=1 caps the actual run
	if _, err := Compile(dir, CompileOpts{MaxDocs: 1}); err != nil {
		var le *limits.LimitError
		if errors.As(err, &le) && le.Which == limits.WhichCompileBatch {
			t.Fatalf("MaxDocs-truncated run tripped compile_batch (limit %d, got %d) — check must count post-truncation work (issue #186)", le.Limit, le.Got)
		}
		t.Fatalf("compile: %v", err)
	}
}
