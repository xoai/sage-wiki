package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #128: aliases persist and union on re-add; Clone is independent.
func TestConceptAliasesUnionAndClone(t *testing.T) {
	m := New()
	m.AddConcept("remedial-action-plan", "wiki/concepts/remedial-action-plan.md", []string{"raw/a.md"}, "rap")
	m.AddConcept("remedial-action-plan", "wiki/concepts/remedial-action-plan.md", []string{"raw/b.md"}, "RAP")
	c := m.Concepts["remedial-action-plan"]
	if len(c.Sources) != 2 || len(c.Aliases) != 2 {
		t.Errorf("union on re-add: sources=%v aliases=%v", c.Sources, c.Aliases)
	}
	// No duplicates.
	m.AddConcept("remedial-action-plan", "wiki/concepts/remedial-action-plan.md", []string{"raw/a.md"}, "rap")
	c = m.Concepts["remedial-action-plan"]
	if len(c.Sources) != 2 || len(c.Aliases) != 2 {
		t.Errorf("dedup on re-add: sources=%v aliases=%v", c.Sources, c.Aliases)
	}

	clone := m.Clone()
	clone.Concepts["remedial-action-plan"].Aliases[0] = "MUTATED"
	if m.Concepts["remedial-action-plan"].Aliases[0] == "MUTATED" {
		t.Error("Clone shares the Aliases backing array with the original")
	}
}

// Old manifests (no aliases field) load with nil Aliases.
func TestOldManifestWithoutAliasesLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")
	os.WriteFile(path, []byte(`{"version":2,"sources":{},"concepts":{"a":{"article_path":"wiki/concepts/a.md","sources":["raw/x.md"],"last_compiled":"2026-01-01T00:00:00Z"}}}`+"\n"), 0o644)
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Concepts["a"].Aliases != nil {
		t.Errorf("old manifest must load nil aliases, got %v", m.Concepts["a"].Aliases)
	}
}
