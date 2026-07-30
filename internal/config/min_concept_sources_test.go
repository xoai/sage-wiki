package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMinConceptSourcesGate(t *testing.T) {
	var c CompilerConfig
	if got := c.MinConceptSourcesOrDefault(); got != 1 {
		t.Errorf("nil → %d, want 1", got)
	}
	zero := 0
	c.MinConceptSources = &zero
	if got := c.MinConceptSourcesOrDefault(); got != 0 {
		t.Errorf("explicit 0 → %d, want 0 (disabled)", got)
	}
	three := 3
	c.MinConceptSources = &three
	if got := c.MinConceptSourcesOrDefault(); got != 3 {
		t.Errorf("explicit 3 → %d", got)
	}
}

func TestMinConceptSourcesYaml(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("project: t\noutput: wiki\nsources:\n  - path: raw\ncompiler:\n  min_concept_sources: 2\n"), 0o644)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Compiler.MinConceptSources == nil || *cfg.Compiler.MinConceptSources != 2 {
		t.Errorf("parsed = %+v", cfg.Compiler.MinConceptSources)
	}
}
