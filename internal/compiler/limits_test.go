package compiler

import (
	"errors"
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
