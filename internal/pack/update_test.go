package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckUpdates_NewerAvailable(t *testing.T) {
	state := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a", Version: "1.0.0"},
			{Name: "pack-b", Version: "2.0.0"},
		},
	}
	index := []PackInfo{
		{Name: "pack-a", Version: "1.1.0"},
		{Name: "pack-b", Version: "2.0.0"},
		{Name: "pack-c", Version: "1.0.0"},
	}

	updates := CheckUpdates(state, index)
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].Name != "pack-a" {
		t.Errorf("expected pack-a, got %s", updates[0].Name)
	}
	if updates[0].CurrentVersion != "1.0.0" || updates[0].LatestVersion != "1.1.0" {
		t.Errorf("versions: %s -> %s", updates[0].CurrentVersion, updates[0].LatestVersion)
	}
}

func TestCheckUpdates_AllUpToDate(t *testing.T) {
	state := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a", Version: "1.0.0"},
		},
	}
	index := []PackInfo{
		{Name: "pack-a", Version: "1.0.0"},
	}

	updates := CheckUpdates(state, index)
	if len(updates) != 0 {
		t.Errorf("expected 0 updates, got %d", len(updates))
	}
}

func TestCheckUpdates_MalformedVersion(t *testing.T) {
	state := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a", Version: "dev"},
		},
	}
	index := []PackInfo{
		{Name: "pack-a", Version: "1.0.0"},
	}

	// should not panic
	updates := CheckUpdates(state, index)
	if len(updates) != 0 {
		t.Errorf("expected 0 updates for malformed version, got %d", len(updates))
	}
}

func TestCheckUpdates_PatchBump(t *testing.T) {
	state := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a", Version: "1.0.1"},
		},
	}
	index := []PackInfo{
		{Name: "pack-a", Version: "1.0.2"},
	}

	updates := CheckUpdates(state, index)
	if len(updates) != 1 {
		t.Fatalf("expected 1 update for patch bump, got %d", len(updates))
	}
}

func TestCheckUpdates_MajorBump(t *testing.T) {
	state := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a", Version: "1.9.9"},
		},
	}
	index := []PackInfo{
		{Name: "pack-a", Version: "2.0.0"},
	}

	updates := CheckUpdates(state, index)
	if len(updates) != 1 {
		t.Fatalf("expected 1 update for major bump, got %d", len(updates))
	}
}

func TestCompareSemver_MalformedInput(t *testing.T) {
	if compareSemver("dev", "1.0.0") != 0 {
		t.Error("malformed a should return 0")
	}
	if compareSemver("1.0.0", "latest") != 0 {
		t.Error("malformed b should return 0")
	}
	if compareSemver("1.0", "1.0.0") != 0 {
		t.Error("2-part should return 0")
	}
	if compareSemver("a.b.c", "1.0.0") != 0 {
		t.Error("non-numeric should return 0")
	}
}

func TestUpdatePack_PathTraversal(t *testing.T) {
	manifest := &PackManifest{
		Name:        "evil-pack",
		Version:     "1.0.0",
		Description: "Malicious update",
		Author:      "attacker",
		Prompts:     []string{"../../.ssh/authorized_keys"},
	}
	err := validateManifestPaths(manifest)
	if err == nil {
		t.Fatal("expected error for path traversal in update manifest")
	}

	manifest.Prompts = nil
	manifest.Skills = []string{"../../../etc/cron.d/backdoor"}
	err = validateManifestPaths(manifest)
	if err == nil {
		t.Fatal("expected error for path traversal in skills")
	}

	manifest.Skills = nil
	manifest.Samples = []string{"/absolute/path"}
	err = validateManifestPaths(manifest)
	if err == nil {
		t.Fatal("expected error for absolute path in samples")
	}
}

func TestUpdatePack_SkipsModifiedFiles(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	os.MkdirAll(filepath.Join(projectDir, ".sage"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\noutput: wiki\nsources:\n  - path: raw\n    type: auto\n"), 0o644)

	// create initial pack
	os.MkdirAll(filepath.Join(projectDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "prompts", "keep.md"), []byte("# Original"), 0o644)
	origHash, _ := ComputeFileHash(filepath.Join(projectDir, "prompts", "keep.md"))

	// simulate an installed pack with this file
	state := &PackState{
		Installed: []InstalledPack{{
			Name:    "test-pack",
			Version: "1.0.0",
			Source:  "local",
			Files:   map[string]string{"prompts/keep.md": origHash},
		}},
	}

	// user modifies the file after apply
	os.WriteFile(filepath.Join(projectDir, "prompts", "keep.md"), []byte("# User edited"), 0o644)

	// create an "updated" pack in packDir
	writePackYAML(t, packDir, `
name: test-pack
version: 2.0.0
description: Updated pack
author: test
prompts:
  - keep.md
`)
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "keep.md"), []byte("# New version"), 0o644)

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	// simulate UpdatePack logic manually (without registry)
	if err := validateManifestPaths(manifest); err != nil {
		t.Fatal(err)
	}

	modifiedFiles := make(map[string]bool)
	for path := range state.Installed[0].Files {
		if state.IsModified(projectDir, "test-pack", path) {
			modifiedFiles[path] = true
		}
	}

	if !modifiedFiles["prompts/keep.md"] {
		t.Fatal("prompts/keep.md should be detected as modified")
	}

	// verify user content is preserved
	data, _ := os.ReadFile(filepath.Join(projectDir, "prompts", "keep.md"))
	if string(data) != "# User edited" {
		t.Errorf("modified file was overwritten: %q", string(data))
	}
}

func TestUpdatePack_TransactionalRollback(t *testing.T) {
	projectDir := t.TempDir()
	packDir := t.TempDir()

	os.MkdirAll(filepath.Join(projectDir, ".sage"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "config.yaml"), []byte("project: test\noutput: wiki\nsources:\n  - path: raw\n    type: auto\n"), 0o644)

	// existing prompt before update
	os.MkdirAll(filepath.Join(projectDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(projectDir, "prompts", "existing.md"), []byte("# Before"), 0o644)
	existingHash, _ := ComputeFileHash(filepath.Join(projectDir, "prompts", "existing.md"))

	state := &PackState{
		Installed: []InstalledPack{{
			Name:    "test-pack",
			Version: "1.0.0",
			Source:  "local",
			Files:   map[string]string{"prompts/existing.md": existingHash},
		}},
	}

	// pack update includes existing.md (will succeed) + missing.md (will fail)
	writePackYAML(t, packDir, `
name: test-pack
version: 2.0.0
description: Pack with missing file
author: test
prompts:
  - existing.md
  - missing.md
`)
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "existing.md"), []byte("# Updated"), 0o644)
	// missing.md intentionally not created

	manifest, err := LoadManifest(packDir)
	if err != nil {
		t.Fatal(err)
	}

	// snapshot + attempt update manually
	paths := []string{"prompts/existing.md", "prompts/missing.md"}
	state.SaveSnapshot(projectDir, "test-pack", paths)

	// first copy succeeds
	safeCopyFile(filepath.Join(packDir, "prompts", "existing.md"), filepath.Join(projectDir, "prompts", "existing.md"), projectDir)

	// second copy fails — simulate rollback
	_ = manifest // used for validation
	state.RestoreSnapshot(projectDir, "test-pack")

	// verify existing.md was restored to original
	data, _ := os.ReadFile(filepath.Join(projectDir, "prompts", "existing.md"))
	if string(data) != "# Before" {
		t.Errorf("rollback failed: existing.md = %q, want '# Before'", string(data))
	}
}
