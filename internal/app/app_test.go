package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/log"
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
	// A subsequent Open ON THE SAME PROJECT succeeds once config exists
	// (spec Tests §2: no poisoned state from the failed Open).
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: 1\nproject: test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	a2, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open on same project: %v", err)
	}
	if err := a2.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestEmbedder_Lazy(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	// Lazy proven by OUTPUT, not internals (a white-box nil check would miss
	// an eager build that probes and returns nil): Open must emit NO
	// embedding-provider output at all — the offline warn and the Ollama
	// probe both happen inside NewFromConfig. internal/log binds os.Stderr
	// at SetVerbosity time; capture via pipe swap (P1-4 mechanism).
	r, w, _ := os.Pipe()
	oldErr := os.Stderr
	os.Stderr = w
	log.SetVerbosity(0)
	// Restore FIRST, unconditionally — a t.Fatalf below must not leave the
	// process logger bound to a pipe with no reader (64KB buffer deadlock
	// across the whole test binary; Gate-8 recheck).
	defer func() {
		os.Stderr = oldErr
		log.SetVerbosity(0)
		w.Close()
	}()

	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer a.Close()

	os.Stderr = oldErr
	log.SetVerbosity(0)
	w.Close()
	out, _ := io.ReadAll(r)
	if strings.Contains(string(out), "embedding provider") || strings.Contains(string(out), "ollama") {
		t.Errorf("Open produced embedder output — must be lazy (spec D1): %s", out)
	}
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
