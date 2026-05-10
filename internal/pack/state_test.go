package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadState_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Installed) != 0 {
		t.Errorf("expected empty state, got %d installed", len(s.Installed))
	}
}

func TestState_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)

	s := &PackState{
		Installed: []InstalledPack{
			{
				Name:    "test-pack",
				Version: "1.0.0",
				Source:  "local",
				Files: map[string]string{
					"prompts/test.md": "sha256:abc123",
				},
			},
		},
	}

	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(loaded.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(loaded.Installed))
	}
	p := loaded.Installed[0]
	if p.Name != "test-pack" {
		t.Errorf("name = %q, want test-pack", p.Name)
	}
	if p.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", p.Version)
	}
	if p.Files["prompts/test.md"] != "sha256:abc123" {
		t.Errorf("file hash = %q", p.Files["prompts/test.md"])
	}
}

func TestState_RecordInstall(t *testing.T) {
	s := &PackState{}

	files := map[string]string{"prompts/a.md": "sha256:111"}
	s.RecordInstall("pack-a", "1.0.0", "registry", files)
	if len(s.Installed) != 1 {
		t.Fatalf("expected 1 installed, got %d", len(s.Installed))
	}

	// update existing
	files2 := map[string]string{"prompts/a.md": "sha256:222"}
	s.RecordInstall("pack-a", "1.1.0", "registry", files2)
	if len(s.Installed) != 1 {
		t.Errorf("expected 1 installed after update, got %d", len(s.Installed))
	}
	if s.Installed[0].Version != "1.1.0" {
		t.Errorf("version not updated: %q", s.Installed[0].Version)
	}
}

func TestState_FindAndRemove(t *testing.T) {
	s := &PackState{
		Installed: []InstalledPack{
			{Name: "pack-a"},
			{Name: "pack-b"},
		},
	}

	p, idx := s.FindInstalled("pack-a")
	if p == nil || idx != 0 {
		t.Errorf("FindInstalled(pack-a) = %v, %d", p, idx)
	}

	_, idx = s.FindInstalled("pack-c")
	if idx != -1 {
		t.Errorf("FindInstalled(pack-c) should return -1, got %d", idx)
	}

	if !s.RemoveInstalled("pack-a") {
		t.Error("RemoveInstalled(pack-a) returned false")
	}
	if len(s.Installed) != 1 {
		t.Errorf("expected 1 installed after remove, got %d", len(s.Installed))
	}
	if s.Installed[0].Name != "pack-b" {
		t.Errorf("remaining pack = %q, want pack-b", s.Installed[0].Name)
	}
}

func TestState_IsModified(t *testing.T) {
	dir := t.TempDir()
	promptsDir := filepath.Join(dir, "prompts")
	os.MkdirAll(promptsDir, 0o755)

	// write a file and compute its hash
	testFile := filepath.Join(promptsDir, "test.md")
	os.WriteFile(testFile, []byte("original content"), 0o644)
	hash, _ := ComputeFileHash(testFile)

	s := &PackState{
		Installed: []InstalledPack{
			{
				Name:  "test-pack",
				Files: map[string]string{"prompts/test.md": hash},
			},
		},
	}

	// not modified
	if s.IsModified(dir, "test-pack", "prompts/test.md") {
		t.Error("file should not be modified")
	}

	// modify the file
	os.WriteFile(testFile, []byte("modified content"), 0o644)
	if !s.IsModified(dir, "test-pack", "prompts/test.md") {
		t.Error("file should be modified")
	}
}

func TestState_SnapshotSaveRestore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)

	// create a file that exists before pack apply
	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	existingFile := filepath.Join(dir, "prompts", "existing.md")
	os.WriteFile(existingFile, []byte("pre-apply content"), 0o644)

	s := &PackState{}

	// snapshot: one existing file, one that doesn't exist
	err := s.SaveSnapshot(dir, "test-pack", []string{"prompts/existing.md", "prompts/new.md"})
	if err != nil {
		t.Fatal(err)
	}

	// verify snapshot was recorded
	p, _ := s.FindInstalled("test-pack")
	if p == nil {
		t.Fatal("pack not found in state after snapshot")
	}
	if p.Snapshots["prompts/existing.md"] == nil {
		t.Error("existing file should have non-nil hash")
	}
	if p.Snapshots["prompts/new.md"] != nil {
		t.Error("missing file should have nil hash")
	}

	// simulate pack changes
	os.WriteFile(existingFile, []byte("modified by pack"), 0o644)
	newFile := filepath.Join(dir, "prompts", "new.md")
	os.WriteFile(newFile, []byte("created by pack"), 0o644)

	// restore
	err = s.RestoreSnapshot(dir, "test-pack")
	if err != nil {
		t.Fatal(err)
	}

	// existing file restored to original
	data, _ := os.ReadFile(existingFile)
	if string(data) != "pre-apply content" {
		t.Errorf("existing file not restored: %q", string(data))
	}

	// new file deleted (didn't exist before)
	if _, err := os.Stat(newFile); err == nil {
		t.Error("new file should have been deleted during restore")
	}
}

func TestComputeFileHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	h1, err := ComputeFileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == "" {
		t.Error("hash should not be empty")
	}
	if h1[:7] != "sha256:" {
		t.Errorf("hash should start with sha256:, got %q", h1[:7])
	}

	// same content = same hash
	h2, _ := ComputeFileHash(path)
	if h1 != h2 {
		t.Error("same file should produce same hash")
	}

	// different content = different hash
	os.WriteFile(path, []byte("world"), 0o644)
	h3, _ := ComputeFileHash(path)
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
}
