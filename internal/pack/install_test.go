package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstall_FromLocal(t *testing.T) {
	packDir := t.TempDir()
	cacheDir := t.TempDir()

	writePackYAML(t, packDir, `
name: local-pack
version: 1.0.0
description: A local test pack
author: test
`)
	// add a prompt file
	os.MkdirAll(filepath.Join(packDir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(packDir, "prompts", "test.md"), []byte("# Test"), 0o644)

	manifest, cachePath, err := Install(packDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	if manifest.Name != "local-pack" {
		t.Errorf("name = %q, want local-pack", manifest.Name)
	}
	if cachePath != filepath.Join(cacheDir, "local-pack") {
		t.Errorf("cachePath = %q", cachePath)
	}

	// verify files copied to cache
	if _, err := os.Stat(filepath.Join(cachePath, "pack.yaml")); err != nil {
		t.Error("pack.yaml not in cache")
	}
	if _, err := os.Stat(filepath.Join(cachePath, "prompts", "test.md")); err != nil {
		t.Error("prompt not in cache")
	}
}

func TestInstall_InvalidPack(t *testing.T) {
	packDir := t.TempDir()
	cacheDir := t.TempDir()

	// pack.yaml with invalid name
	writePackYAML(t, packDir, `
name: INVALID
version: 1.0.0
description: Bad name
author: test
`)

	_, _, err := Install(packDir, cacheDir)
	if err == nil {
		t.Fatal("expected error for invalid pack")
	}
}

func TestInstall_Overwrite(t *testing.T) {
	packDir := t.TempDir()
	cacheDir := t.TempDir()

	writePackYAML(t, packDir, `
name: overwrite-pack
version: 1.0.0
description: First version
author: test
`)

	Install(packDir, cacheDir)

	// install again with updated version
	writePackYAML(t, packDir, `
name: overwrite-pack
version: 2.0.0
description: Second version
author: test
`)

	manifest, _, err := Install(packDir, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", manifest.Version)
	}
}

func TestIsInstalled(t *testing.T) {
	cacheDir := t.TempDir()

	if IsInstalled("nonexistent", cacheDir) {
		t.Error("should not be installed")
	}

	// install a pack
	packDir := t.TempDir()
	writePackYAML(t, packDir, `
name: installed-pack
version: 1.0.0
description: Test
author: test
`)
	Install(packDir, cacheDir)

	if !IsInstalled("installed-pack", cacheDir) {
		t.Error("should be installed")
	}
}

func TestInstalledPath(t *testing.T) {
	cacheDir := "/tmp/test-cache"
	path := InstalledPath("my-pack", cacheDir)
	if path != "/tmp/test-cache/my-pack" {
		t.Errorf("path = %q", path)
	}
}

func TestInstall_MinVersionCheck(t *testing.T) {
	origVersion := Version
	defer func() { Version = origVersion }()

	packDir := t.TempDir()
	cacheDir := t.TempDir()

	Version = "0.5.0"
	writePackYAML(t, packDir, `
name: new-pack
version: 1.0.0
description: Requires newer version
author: test
min_version: 1.0.0
`)

	_, _, err := Install(packDir, cacheDir)
	if err == nil {
		t.Fatal("expected error for min_version check")
	}
}
