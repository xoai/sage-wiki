package pack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"gopkg.in/yaml.v3"
)

func TestPackMerge_FillOnly(t *testing.T) {
	base := map[string]any{
		"project": "my-wiki",
		"output":  "wiki",
		"empty":   "",
		"nil_val": nil,
	}
	overlay := map[string]any{
		"project": "overridden-wiki",
		"output":  "custom-output",
		"empty":   "filled",
		"new_key": "added",
		"nil_val": "filled-nil",
	}
	result := PackMerge(base, overlay)

	// user value preserved — even empty string is a user value
	if result["project"] != "my-wiki" {
		t.Errorf("project = %v, want my-wiki (user value preserved)", result["project"])
	}
	if result["output"] != "wiki" {
		t.Errorf("output = %v, want wiki (user value preserved)", result["output"])
	}
	// explicit empty string is user-set — NOT filled
	if result["empty"] != "" {
		t.Errorf("empty = %v, want empty string (user-set value preserved)", result["empty"])
	}
	// nil value IS filled
	if result["nil_val"] != "filled-nil" {
		t.Errorf("nil_val = %v, want filled-nil", result["nil_val"])
	}
	// new key added
	if result["new_key"] != "added" {
		t.Errorf("new_key = %v, want added", result["new_key"])
	}
}

func TestPackMerge_NestedMaps(t *testing.T) {
	base := map[string]any{
		"compiler": map[string]any{
			"default_tier": 2,
			"mode":         "",
		},
	}
	overlay := map[string]any{
		"compiler": map[string]any{
			"default_tier": 3,
			"mode":         "batch",
			"max_parallel": 10,
		},
	}
	result := PackMerge(base, overlay)

	compiler := result["compiler"].(map[string]any)
	// existing value preserved
	if compiler["default_tier"] != 2 {
		t.Errorf("default_tier = %v, want 2", compiler["default_tier"])
	}
	// explicit empty string is user-set — NOT filled
	if compiler["mode"] != "" {
		t.Errorf("mode = %v, want empty string (user-set value preserved)", compiler["mode"])
	}
	// new key added
	if compiler["max_parallel"] != 10 {
		t.Errorf("max_parallel = %v, want 10", compiler["max_parallel"])
	}
}

func TestPackMerge_NilAndZeroValues(t *testing.T) {
	base := map[string]any{
		"nil_val":   nil,
		"zero_int":  0,
		"false_val": false,
		"empty_arr": []any{},
	}
	overlay := map[string]any{
		"nil_val":   "filled",
		"zero_int":  42,
		"false_val": true,
		"empty_arr": []any{"a"},
	}
	result := PackMerge(base, overlay)

	// nil is filled
	if result["nil_val"] != "filled" {
		t.Errorf("nil_val = %v, want filled", result["nil_val"])
	}
	// explicit 0 is user-set — NOT overwritten
	if result["zero_int"] != 0 {
		t.Errorf("zero_int = %v, want 0 (user-set value preserved)", result["zero_int"])
	}
	// explicit false is user-set — NOT overwritten
	if result["false_val"] != false {
		t.Errorf("false_val = %v, want false (user-set value preserved)", result["false_val"])
	}
}

func TestMergeOntology_UnionRelations(t *testing.T) {
	base := config.OntologyConfig{
		RelationTypes: []config.RelationConfig{
			{Name: "cites", Synonyms: []string{"references"}},
			{Name: "depends_on"},
		},
		EntityTypes: []config.EntityTypeConfig{
			{Name: "paper"},
		},
	}
	overlay := config.OntologyConfig{
		RelationTypes: []config.RelationConfig{
			{Name: "cites", Synonyms: []string{"cites_work", "references"}},
			{Name: "contradicts"},
		},
		EntityTypes: []config.EntityTypeConfig{
			{Name: "hypothesis", Description: "A research hypothesis"},
			{Name: "paper"}, // duplicate — should not add
		},
	}

	result := MergeOntology(base, overlay)

	// 3 relation types (cites, depends_on, contradicts)
	if len(result.RelationTypes) != 3 {
		t.Fatalf("RelationTypes count = %d, want 3", len(result.RelationTypes))
	}

	// cites synonyms merged (union): references, cites_work
	cites := result.RelationTypes[0]
	if cites.Name != "cites" {
		t.Errorf("first relation = %q, want cites", cites.Name)
	}
	if len(cites.Synonyms) != 2 {
		t.Errorf("cites synonyms = %d, want 2 (references, cites_work)", len(cites.Synonyms))
	}

	// new relation added
	if result.RelationTypes[2].Name != "contradicts" {
		t.Errorf("third relation = %q, want contradicts", result.RelationTypes[2].Name)
	}

	// 2 entity types (paper + hypothesis, no duplicate paper)
	if len(result.EntityTypes) != 2 {
		t.Fatalf("EntityTypes count = %d, want 2", len(result.EntityTypes))
	}
}

func TestMergePrompts_NewFiles(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	// create pack prompts
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "new-prompt.md"), []byte("# New"), 0o644)

	added, conflicts, err := MergePrompts(projectDir, packDir, []string{"new-prompt.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 1 {
		t.Errorf("added = %d, want 1", len(added))
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %d, want 0", len(conflicts))
	}

	// verify file exists
	dst := filepath.Join(projectDir, "prompts", "new-prompt.md")
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("prompt file not created: %v", err)
	}
}

func TestMergePrompts_ConflictDetection(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	// create existing project prompt
	os.MkdirAll(filepath.Join(projectDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "prompts", "existing.md"), []byte("# Existing"), 0o644)

	// create pack prompt with same name
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "existing.md"), []byte("# Pack version"), 0o644)

	added, conflicts, err := MergePrompts(projectDir, packDir, []string{"existing.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("added = %d, want 0", len(added))
	}
	if len(conflicts) != 1 {
		t.Errorf("conflicts = %d, want 1", len(conflicts))
	}

	// original file preserved
	data, _ := os.ReadFile(filepath.Join(projectDir, "prompts", "existing.md"))
	if string(data) != "# Existing" {
		t.Errorf("existing prompt was overwritten")
	}
}

func TestApply_TransactionalRollback(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	// create a valid project config
	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	// create pack with a prompt that references a non-existent source file
	writePackYAML(t, packDir, `
name: test-pack
version: 1.0.0
description: Test pack
author: test
prompts:
  - good-prompt.md
  - missing-prompt.md
`)
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "good-prompt.md"), []byte("# Good"), 0o644)
	// missing-prompt.md intentionally not created

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err == nil {
		t.Fatal("expected error from missing prompt file")
	}

	// verify rollback: good-prompt.md should not exist (rolled back)
	if _, err := os.Stat(filepath.Join(projectDir, "prompts", "good-prompt.md")); err == nil {
		t.Error("good-prompt.md should have been rolled back")
	}
}

func TestApply_ConfigMerge(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
compiler:
  default_tier: 2
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	writePackYAML(t, packDir, `
name: test-pack
version: 1.0.0
description: Test pack
author: test
config:
  compiler:
    default_tier: 3
    mode: batch
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	result, err := Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.ConfigChanges) == 0 {
		t.Error("expected config changes")
	}

	// verify: user's default_tier=2 preserved, pack's mode=batch filled
	data, _ := os.ReadFile(filepath.Join(projectDir, "config.yaml"))
	var merged map[string]any
	yaml.Unmarshal(data, &merged)

	compiler := merged["compiler"].(map[string]any)
	if compiler["default_tier"] != 2 {
		t.Errorf("default_tier = %v, want 2 (user value preserved)", compiler["default_tier"])
	}
	if compiler["mode"] != "batch" {
		t.Errorf("mode = %v, want batch (fill-only)", compiler["mode"])
	}
}

func TestApply_ReplaceMode(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
compiler:
  default_tier: 2
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	writePackYAML(t, packDir, `
name: test-pack
version: 1.0.0
description: Test pack
author: test
config:
  compiler:
    default_tier: 3
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeReplace, state)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(projectDir, "config.yaml"))
	var merged map[string]any
	yaml.Unmarshal(data, &merged)

	compiler := merged["compiler"].(map[string]any)
	if compiler["default_tier"] != 3 {
		t.Errorf("default_tier = %v, want 3 (replace mode overwrites)", compiler["default_tier"])
	}
}
