package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_ValidPack(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "test-pack")
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.MkdirAll(filepath.Join(packDir, "samples"), 0o755)

	os.WriteFile(filepath.Join(packDir, "prompts", "test.md"), []byte("# Prompt"), 0o644)
	os.WriteFile(filepath.Join(packDir, "samples", "sample.md"), []byte("# Sample"), 0o644)

	writePackYAML(t, packDir, `
name: test-pack
version: 1.0.0
description: A valid test pack
author: test
prompts:
  - test.md
samples:
  - sample.md
ontology:
  relation_types:
    - name: cites
  entity_types:
    - name: paper
`)

	errors, err := Validate(packDir)
	if err != nil {
		t.Fatal(err)
	}
	// filter errors only (not warnings)
	var errs []ValidationError
	for _, e := range errors {
		if e.Level == "error" {
			errs = append(errs, e)
		}
	}
	if len(errs) > 0 {
		t.Errorf("expected no errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_MissingFiles(t *testing.T) {
	dir := t.TempDir()

	writePackYAML(t, dir, `
name: test-pack
version: 1.0.0
description: Missing files
author: test
prompts:
  - nonexistent.md
skills:
  - missing-skill.md
`)

	errors, err := Validate(dir)
	if err != nil {
		t.Fatal(err)
	}

	fileErrors := 0
	for _, e := range errors {
		if e.Level == "error" && (e.Field == "prompts" || e.Field == "skills") {
			fileErrors++
		}
	}
	if fileErrors < 2 {
		t.Errorf("expected at least 2 file errors, got %d", fileErrors)
	}
}

func TestValidate_PathTraversal(t *testing.T) {
	dir := t.TempDir()

	writePackYAML(t, dir, `
name: test-pack
version: 1.0.0
description: Path traversal
author: test
prompts:
  - "../../escape.md"
`)

	errors, err := Validate(dir)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range errors {
		if e.Level == "error" && e.Field == "paths" {
			found = true
		}
	}
	if !found {
		t.Error("expected path traversal error")
	}
}

func TestValidate_InvalidOntologyName(t *testing.T) {
	dir := t.TempDir()

	writePackYAML(t, dir, `
name: test-pack
version: 1.0.0
description: Bad ontology
author: test
ontology:
  relation_types:
    - name: "Invalid-Name"
`)

	errors, err := Validate(dir)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, e := range errors {
		if e.Level == "error" && e.Field == "ontology.relation_types" {
			found = true
		}
	}
	if !found {
		t.Error("expected ontology name validation error")
	}
}

func TestValidateOntologyName(t *testing.T) {
	valid := []string{"cites", "depends_on", "a1"}
	for _, n := range valid {
		if err := validateOntologyName(n); err != nil {
			t.Errorf("validateOntologyName(%q) = %v, want nil", n, err)
		}
	}

	invalid := []string{"", "123", "Has-Dash", "UPPER", "-start"}
	for _, n := range invalid {
		if err := validateOntologyName(n); err == nil {
			t.Errorf("validateOntologyName(%q) = nil, want error", n)
		}
	}
}
