package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func writePackYAML(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: academic-research
version: 1.0.0
description: Pack for academic research workflows
author: sage-wiki
license: MIT
min_version: 0.5.0
tags: [research, academic]
config:
  compiler:
    default_tier: 3
ontology:
  relation_types:
    - name: cites
      synonyms: [references, cites_work]
    - name: contradicts
  entity_types:
    - name: hypothesis
      description: A research hypothesis
prompts:
  - summarize-article.md
  - extract-concepts.md
samples:
  - example-paper.md
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Name != "academic-research" {
		t.Errorf("Name = %q, want academic-research", m.Name)
	}
	if m.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", m.Version)
	}
	if m.License != "MIT" {
		t.Errorf("License = %q, want MIT", m.License)
	}
	if len(m.Tags) != 2 {
		t.Errorf("Tags count = %d, want 2", len(m.Tags))
	}
	if len(m.Ontology.RelationTypes) != 2 {
		t.Errorf("RelationTypes count = %d, want 2", len(m.Ontology.RelationTypes))
	}
	if m.Ontology.RelationTypes[0].Name != "cites" {
		t.Errorf("RelationTypes[0].Name = %q, want cites", m.Ontology.RelationTypes[0].Name)
	}
	if len(m.Ontology.RelationTypes[0].Synonyms) != 2 {
		t.Errorf("cites synonyms count = %d, want 2", len(m.Ontology.RelationTypes[0].Synonyms))
	}
	if len(m.Ontology.EntityTypes) != 1 {
		t.Errorf("EntityTypes count = %d, want 1", len(m.Ontology.EntityTypes))
	}
	if len(m.Prompts) != 2 {
		t.Errorf("Prompts count = %d, want 2", len(m.Prompts))
	}
	if m.Config["compiler"] == nil {
		t.Error("Config.compiler is nil")
	}
}

func TestLoadManifest_MissingName(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
version: 1.0.0
description: No name pack
author: test
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadManifest_MissingVersion(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: test-pack
description: No version
author: test
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestLoadManifest_MissingDescription(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: test-pack
version: 1.0.0
author: test
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestLoadManifest_MissingAuthor(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: test-pack
version: 1.0.0
description: A test pack
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing author")
	}
}

func TestLoadManifest_MissingFile(t *testing.T) {
	_, err := LoadManifest(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing pack.yaml")
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"my-pack", "a", "academic-research", "pack123", "a1-b2"}
	for _, n := range valid {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{"", "My-Pack", "123pack", "-start", "has_underscore", "has space", "UPPER"}
	for _, n := range invalid {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestValidateVersion(t *testing.T) {
	valid := []string{"0.0.0", "1.0.0", "12.34.56"}
	for _, v := range valid {
		if err := ValidateVersion(v); err != nil {
			t.Errorf("ValidateVersion(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{"", "1.0", "1.0.0.0", "v1.0.0", "a.b.c", "1.0.x"}
	for _, v := range invalid {
		if err := ValidateVersion(v); err == nil {
			t.Errorf("ValidateVersion(%q) = nil, want error", v)
		}
	}
}

func TestCheckMinVersion(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	// dev always passes
	Version = "dev"
	if err := CheckMinVersion("99.0.0"); err != nil {
		t.Errorf("dev version should always pass: %v", err)
	}

	// empty min_version always passes
	Version = "1.0.0"
	if err := CheckMinVersion(""); err != nil {
		t.Errorf("empty min_version should pass: %v", err)
	}

	// current >= required
	Version = "1.2.0"
	if err := CheckMinVersion("1.2.0"); err != nil {
		t.Errorf("equal version should pass: %v", err)
	}
	if err := CheckMinVersion("1.1.0"); err != nil {
		t.Errorf("newer version should pass: %v", err)
	}

	// current < required
	Version = "0.9.0"
	if err := CheckMinVersion("1.0.0"); err == nil {
		t.Error("older version should fail")
	}

	// patch version comparison
	Version = "1.0.1"
	if err := CheckMinVersion("1.0.2"); err == nil {
		t.Error("older patch should fail")
	}
}

func TestLoadManifest_InvalidName(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: Invalid-Name
version: 1.0.0
description: Bad name
author: test
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for invalid name")
	}
}

func TestLoadManifest_InvalidVersion(t *testing.T) {
	dir := t.TempDir()
	writePackYAML(t, dir, `
name: test-pack
version: not-a-version
description: Bad version
author: test
`)
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected error for invalid version")
	}
}
