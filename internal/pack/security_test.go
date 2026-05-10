package pack

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestApply_PathTraversal_Prompts(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\n"), 0o644)

	writePackYAML(t, packDir, `
name: evil-pack
version: 1.0.0
description: Malicious pack
author: attacker
prompts:
  - "../../.bashrc"
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err == nil {
		t.Fatal("expected error for path traversal in prompts")
	}
}

func TestApply_PathTraversal_Skills(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\n"), 0o644)

	writePackYAML(t, packDir, `
name: evil-pack
version: 1.0.0
description: Malicious pack
author: attacker
skills:
  - "../../../etc/cron.d/backdoor"
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err == nil {
		t.Fatal("expected error for path traversal in skills")
	}
}

func TestApply_PathTraversal_AbsolutePath(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\n"), 0o644)

	writePackYAML(t, packDir, `
name: evil-pack
version: 1.0.0
description: Malicious pack
author: attacker
prompts:
  - "/etc/passwd"
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err == nil {
		t.Fatal("expected error for absolute path in prompts")
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"file.md", "subdir/file.md", "a/b/c.txt"}
	for _, p := range valid {
		if err := ValidateRelPath(p); err != nil {
			t.Errorf("ValidateRelPath(%q) = %v, want nil", p, err)
		}
	}

	invalid := []string{"../escape", "../../etc/passwd", "/absolute/path", "../"}
	for _, p := range invalid {
		if err := ValidateRelPath(p); err == nil {
			t.Errorf("ValidateRelPath(%q) = nil, want error", p)
		}
	}
}

func TestValidateGitURL(t *testing.T) {
	valid := []string{
		"https://github.com/user/repo",
		"git://github.com/user/repo",
		"ssh://git@github.com/user/repo",
		"git@github.com:user/repo.git",
	}
	for _, u := range valid {
		if err := validateGitURL(u); err != nil {
			t.Errorf("validateGitURL(%q) = %v, want nil", u, err)
		}
	}

	invalid := []string{
		"--upload-pack=evil",
		"ext::sh -c evil%",
		"-evil",
	}
	for _, u := range invalid {
		if err := validateGitURL(u); err == nil {
			t.Errorf("validateGitURL(%q) = nil, want error", u)
		}
	}
}

func TestApply_ConfigAndOntologyTogether(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
compiler:
  default_tier: 2
ontology:
  relation_types:
    - name: existing_relation
      synonyms: [old_synonym]
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	writePackYAML(t, packDir, `
name: combo-pack
version: 1.0.0
description: Pack with both config and ontology
author: test
config:
  compiler:
    mode: batch
ontology:
  relation_types:
    - name: existing_relation
      synonyms: [new_synonym]
    - name: new_relation
  entity_types:
    - name: hypothesis
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

	// verify config changes
	if len(result.ConfigChanges) == 0 {
		t.Error("expected config changes")
	}
	if len(result.OntologyAdded) == 0 {
		t.Error("expected ontology additions")
	}

	// read back and verify both config and ontology survived
	data, _ := os.ReadFile(filepath.Join(projectDir, "config.yaml"))
	var merged map[string]any
	yaml.Unmarshal(data, &merged)

	// config: user value preserved, pack value filled
	compiler := merged["compiler"].(map[string]any)
	if compiler["default_tier"] != 2 {
		t.Errorf("default_tier = %v, want 2 (user value preserved)", compiler["default_tier"])
	}
	if compiler["mode"] != "batch" {
		t.Errorf("mode = %v, want batch (fill-only from pack)", compiler["mode"])
	}

	// ontology: union merge worked
	ontology := merged["ontology"].(map[string]any)
	relationTypes := ontology["relation_types"].([]any)
	if len(relationTypes) != 2 {
		t.Fatalf("relation_types count = %d, want 2", len(relationTypes))
	}

	// existing_relation should have both old and new synonyms
	first := relationTypes[0].(map[string]any)
	synonyms := first["synonyms"].([]any)
	if len(synonyms) != 2 {
		t.Errorf("existing_relation synonyms = %d, want 2 (old_synonym + new_synonym)", len(synonyms))
	}

	// entity_types should have hypothesis
	entityTypes := ontology["entity_types"].([]any)
	if len(entityTypes) != 1 {
		t.Errorf("entity_types count = %d, want 1", len(entityTypes))
	}
}

func TestApply_SnapshotPreservedForRemove(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\n"), 0o644)

	writePackYAML(t, packDir, `
name: snapshot-pack
version: 1.0.0
description: Test pack
author: test
prompts:
  - new-prompt.md
`)
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "new-prompt.md"), []byte("# New"), 0o644)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err != nil {
		t.Fatal(err)
	}

	// verify snapshot directory still exists (not cleaned)
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", "snapshot-pack")
	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		t.Error("snapshot directory should be preserved for pack remove")
	}

	// verify pack state records snapshots
	p, _ := state.FindInstalled("snapshot-pack")
	if p == nil {
		t.Fatal("pack not in state")
	}
	if p.Snapshots == nil {
		t.Error("snapshots should be recorded in state")
	}
}

func TestApply_DenylistsParsersTrustKeys(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
parsers:
  external: false
  trust_external: false
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	// malicious pack tries to set parsers.trust_external: true
	writePackYAML(t, packDir, `
name: evil-trust-pack
version: 1.0.0
description: Tries to enable parser trust
author: attacker
config:
  parsers:
    external: true
    trust_external: true
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err != nil {
		t.Fatal(err)
	}

	// verify parsers keys were NOT set by the pack
	data, _ := os.ReadFile(filepath.Join(projectDir, "config.yaml"))
	var merged map[string]any
	yaml.Unmarshal(data, &merged)

	parsers, ok := merged["parsers"].(map[string]any)
	if ok {
		if ext, ok := parsers["external"].(bool); ok && ext {
			t.Error("pack should NOT be able to set parsers.external to true")
		}
		if trust, ok := parsers["trust_external"].(bool); ok && trust {
			t.Error("pack should NOT be able to set parsers.trust_external to true")
		}
	}
}

func TestApply_RecordsConfigHashInFiles(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	cfgData := `project: test
output: wiki
sources:
  - path: raw
    type: auto
`
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte(cfgData), 0o644)

	writePackYAML(t, packDir, `
name: config-pack
version: 1.0.0
description: Pack with config changes
author: test
config:
  compiler:
    mode: batch
`)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err != nil {
		t.Fatal(err)
	}

	// verify config.yaml hash is recorded in Files
	installed, _ := state.FindInstalled("config-pack")
	if installed == nil {
		t.Fatal("pack not in state")
	}
	configHash, ok := installed.Files["config.yaml"]
	if !ok || configHash == "" {
		t.Error("config.yaml hash should be recorded in installed.Files")
	}
}

func TestApply_NoConfigSnapshot_WhenNoConfigChanges(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\n"), 0o644)

	// pack with NO config or ontology — only prompts
	writePackYAML(t, packDir, `
name: prompt-only-pack
version: 1.0.0
description: No config changes
author: test
prompts:
  - test.md
`)
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "test.md"), []byte("# Test"), 0o644)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	state := &PackState{}
	_, err = Apply(projectDir, packDir, manifest, ModeMerge, state)
	if err != nil {
		t.Fatal(err)
	}

	// verify config.yaml is NOT in snapshots (pack didn't modify it)
	installed, _ := state.FindInstalled("prompt-only-pack")
	if installed == nil {
		t.Fatal("pack not in state")
	}
	if _, hasConfigSnapshot := installed.Snapshots["config.yaml"]; hasConfigSnapshot {
		t.Error("config.yaml should NOT be snapshotted when pack doesn't modify config")
	}
	// and NOT in Files
	if _, hasConfigFile := installed.Files["config.yaml"]; hasConfigFile {
		t.Error("config.yaml should NOT be in Files when pack doesn't modify config")
	}
}

func TestStripDenylisted(t *testing.T) {
	overlay := map[string]any{
		"compiler": map[string]any{"mode": "batch"},
		"parsers":  map[string]any{"external": true, "trust_external": true},
		"api":      map[string]any{"key": "stolen"},
		"embed":    map[string]any{"base_url": "https://evil.com"},
		"models":   map[string]any{"summarize": "evil-model"},
		"search":   map[string]any{"hybrid_weight_bm25": 0.8},
	}

	safe, stripped := stripDenylisted(overlay)

	if len(stripped) != 4 {
		t.Errorf("expected 4 stripped keys (parsers, api, embed, models), got %d: %v", len(stripped), stripped)
	}
	if _, ok := safe["parsers"]; ok {
		t.Error("parsers should be stripped")
	}
	if _, ok := safe["api"]; ok {
		t.Error("api should be stripped")
	}
	if _, ok := safe["embed"]; ok {
		t.Error("embed should be stripped")
	}
	if _, ok := safe["models"]; ok {
		t.Error("models should be stripped")
	}
	if _, ok := safe["compiler"]; !ok {
		t.Error("compiler should NOT be stripped")
	}
	if _, ok := safe["search"]; !ok {
		t.Error("search should NOT be stripped")
	}
}
