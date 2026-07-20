package app

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func TestOpen_Greenfield(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if a.Config == nil || a.DB == nil || a.Mem == nil || a.Vec == nil || a.Ont == nil || a.Searcher == nil {
		t.Error("Open returned a partially wired App")
	}

	// Stores are usable: write via one, read via another.
	if err := a.Mem.Add(memory.Entry{ID: "concept:x", Content: "test content"}); err != nil {
		t.Errorf("Mem store unusable: %v", err)
	}
	if n, _ := a.Mem.Count(); n != 1 {
		t.Errorf("Mem count = %d, want 1", n)
	}

	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close must be idempotent: %v", err)
	}
}

func TestOpen_MissingConfig(t *testing.T) {
	dir := t.TempDir() // no config.yaml
	a, err := Open(dir)
	if err == nil {
		t.Fatal("expected error for missing config.yaml")
	}
	if a != nil {
		t.Error("App must be nil on error")
	}
	// A subsequent Open with a valid project succeeds (no leaked state).
	dir2 := t.TempDir()
	wiki.InitGreenfield(dir2, "test", "gpt-4o-mini")
	a2, err := Open(dir2)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	a2.Close()
}

func TestEmbedder_Lazy(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	// Lazy: Open must not have built an embedder (no Ollama probe fired at
	// Open time — embedder field is nil until first Embedder() call).
	if a.embedder != nil {
		t.Error("embedder built eagerly at Open — must be lazy (spec D1)")
	}
	// First call builds (may be nil when no provider is configured — that's
	// embed.NewFromConfig's own semantics, not an error); second call
	// returns the same instance.
	e1 := a.Embedder()
	e2 := a.Embedder()
	if e1 != e2 {
		t.Error("Embedder() must cache the first-built instance")
	}
}
