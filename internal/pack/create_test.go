package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateScaffold(t *testing.T) {
	dir := t.TempDir()

	err := CreateScaffold("my-pack", dir)
	if err != nil {
		t.Fatal(err)
	}

	packDir := filepath.Join(dir, "my-pack")

	// verify structure
	expected := []string{
		"pack.yaml",
		"prompts/example-prompt.md",
		"samples/example-source.md",
		"README.md",
	}
	for _, f := range expected {
		if _, err := os.Stat(filepath.Join(packDir, f)); err != nil {
			t.Errorf("missing expected file: %s", f)
		}
	}

	// verify pack.yaml is valid
	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatalf("scaffold pack.yaml is invalid: %v", err)
	}
	if manifest.Name != "my-pack" {
		t.Errorf("name = %q, want my-pack", manifest.Name)
	}
}

func TestCreateScaffold_InvalidName(t *testing.T) {
	err := CreateScaffold("INVALID", t.TempDir())
	if err == nil {
		t.Error("expected error for invalid name")
	}
}

func TestCreateScaffold_AlreadyExists(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "existing-pack"), 0o755)

	err := CreateScaffold("existing-pack", dir)
	if err == nil {
		t.Error("expected error for existing directory")
	}
}

func TestCreateScaffoldFromProject(t *testing.T) {
	dir := t.TempDir()
	projectDir := t.TempDir()

	// create project config with ontology
	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
ontology:
  relation_types:
    - name: cites
      synonyms: [references]
  entity_types:
    - name: paper
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	err := CreateScaffoldFromProject("project-pack", dir, projectDir)
	if err != nil {
		t.Fatal(err)
	}

	// verify the ontology was pre-filled
	manifest, err := LoadManifest(filepath.Join(dir, "project-pack"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "project-pack" {
		t.Errorf("name = %q", manifest.Name)
	}
}
