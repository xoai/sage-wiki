package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGreenfield(t *testing.T) {
	dir := t.TempDir()

	if err := InitGreenfield(dir, "test-wiki"); err != nil {
		t.Fatalf("InitGreenfield: %v", err)
	}

	// Verify directory structure
	expectedDirs := []string{
		"raw",
		"wiki/summaries",
		"wiki/concepts",
		"wiki/connections",
		"wiki/outputs",
		"wiki/images",
		"wiki/archive",
		".sage",
	}
	for _, d := range expectedDirs {
		path := filepath.Join(dir, d)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", d)
		}
	}

	// Verify config.yaml
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		t.Error("config.yaml should exist")
	}
	cfgData, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfgData), "embed:") {
		t.Error("config.yaml should include embed section")
	}
	if !strings.Contains(string(cfgData), "provider: auto") {
		t.Error("config.yaml should include default embed provider")
	}

	// Verify .gitignore
	gitignore := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(data) != ".sage/\n" {
		t.Errorf("unexpected .gitignore content: %q", string(data))
	}

	// Verify manifest
	manifestPath := filepath.Join(dir, ".manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		t.Error(".manifest.json should exist")
	}

	// Verify DB
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("wiki.db should exist")
	}
}

func TestInitVaultOverlay(t *testing.T) {
	dir := t.TempDir()

	// Create some vault folders
	os.MkdirAll(filepath.Join(dir, "Clippings"), 0755)
	os.MkdirAll(filepath.Join(dir, "Papers"), 0755)
	os.WriteFile(filepath.Join(dir, "Clippings", "test.md"), []byte("# Test"), 0644)

	err := InitVaultOverlay(dir, "my-vault",
		[]string{"Clippings", "Papers"},
		[]string{"Personal", "Daily Notes"},
		"_wiki",
	)
	if err != nil {
		t.Fatalf("InitVaultOverlay: %v", err)
	}

	// Verify _wiki structure
	expectedDirs := []string{
		"_wiki/summaries",
		"_wiki/concepts",
		"_wiki/connections",
		"_wiki/outputs",
		"_wiki/images",
		"_wiki/archive",
		".sage",
	}
	for _, d := range expectedDirs {
		path := filepath.Join(dir, d)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected directory %s to exist", d)
		}
	}

	// Source folders should NOT be modified
	clippingsTest := filepath.Join(dir, "Clippings", "test.md")
	if _, err := os.Stat(clippingsTest); os.IsNotExist(err) {
		t.Error("source files should not be modified")
	}

	cfgData, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(cfgData), "embed:") {
		t.Error("config.yaml should include embed section")
	}
}

func TestScanFolders(t *testing.T) {
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, "Clippings"), 0755)
	os.MkdirAll(filepath.Join(dir, "Papers"), 0755)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.MkdirAll(filepath.Join(dir, "Empty"), 0755)

	os.WriteFile(filepath.Join(dir, "Clippings", "a.md"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "Clippings", "b.md"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "Papers", "paper.pdf"), []byte("x"), 0644)

	folders, err := ScanFolders(dir)
	if err != nil {
		t.Fatalf("ScanFolders: %v", err)
	}

	// Should not include .hidden
	for _, f := range folders {
		if f.Name == ".hidden" {
			t.Error("should not include hidden folders")
		}
	}

	// Find Clippings
	var clippings *FolderInfo
	for i := range folders {
		if folders[i].Name == "Clippings" {
			clippings = &folders[i]
		}
	}
	if clippings == nil {
		t.Fatal("expected Clippings folder")
	}
	if clippings.FileCount != 2 {
		t.Errorf("expected 2 files in Clippings, got %d", clippings.FileCount)
	}
	if !clippings.HasMD {
		t.Error("Clippings should have .md files")
	}
}
