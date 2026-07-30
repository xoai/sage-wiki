package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #127: re-running init must not destroy user files. config.yaml is
// preserved today; .gitignore and .manifest.json were overwritten
// unconditionally (data loss on compiled vaults).

func TestInitGreenfieldPreservesUserFiles(t *testing.T) {
	dir := t.TempDir()
	// Seed user content in all three files.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# my notes\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".manifest.json"),
		[]byte(`{"version":2,"sources":{"raw/a.md":{}},"concepts":{"alpha":{}}}`+"\n"), 0o644)

	if err := InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatalf("InitGreenfield: %v", err)
	}

	gitignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(gitignore), "# my notes") {
		t.Errorf(".gitignore user content destroyed: %q", gitignore)
	}
	if !strings.Contains(string(gitignore), ".sage/") {
		t.Errorf(".sage/ not appended to existing .gitignore: %q", gitignore)
	}

	manifest, _ := os.ReadFile(filepath.Join(dir, ".manifest.json"))
	if !strings.Contains(string(manifest), "alpha") {
		t.Errorf(".manifest.json compile history destroyed: %q", manifest)
	}
}

func TestInitGitignoreLineExact(t *testing.T) {
	dir := t.TempDir()
	// A COMMENTED .sage/ line must NOT count as present — append anyway.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# .sage/\ntmp/\n"), 0o644)
	if err := InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	gitignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	lines := strings.Split(strings.TrimSpace(string(gitignore)), "\n")
	var exact int
	for _, l := range lines {
		if strings.TrimSpace(l) == ".sage/" {
			exact++
		}
	}
	if exact != 1 {
		t.Errorf("expected exactly one line-exact .sage/ entry, got %d: %q", exact, gitignore)
	}
}

func TestInitGreenfieldForceRewrites(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# my notes\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".manifest.json"),
		[]byte(`{"version":2,"sources":{"raw/a.md":{}},"concepts":{"alpha":{}}}`+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("# user config\n"), 0o644)

	if err := InitGreenfield(dir, "test", "gpt-4o-mini", WithForce(true)); err != nil {
		t.Fatalf("InitGreenfield(force): %v", err)
	}

	gitignore, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(gitignore) != ".sage/\n" {
		t.Errorf("force must rewrite .gitignore, got %q", gitignore)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, ".manifest.json"))
	if strings.Contains(string(manifest), "alpha") {
		t.Errorf("force must rewrite .manifest.json, got %q", manifest)
	}
	// Force does NOT touch config.yaml — preservation there is unconditional (#84).
	cfg, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if string(cfg) != "# user config\n" {
		t.Errorf("force must not overwrite config.yaml, got %q", cfg)
	}
}

func TestInitVaultOverlayPreservesManifest(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".manifest.json"),
		[]byte(`{"version":2,"sources":{"raw/a.md":{}},"concepts":{"alpha":{}}}`+"\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "notes"), 0o755)

	if err := InitVaultOverlay(dir, "test", []string{"notes"}, nil, "wiki", "gpt-4o-mini"); err != nil {
		t.Fatalf("InitVaultOverlay: %v", err)
	}
	manifest, _ := os.ReadFile(filepath.Join(dir, ".manifest.json"))
	if !strings.Contains(string(manifest), "alpha") {
		t.Errorf("vault overlay destroyed manifest: %q", manifest)
	}
}

// Review: corrupt manifests self-heal on re-init (not preserved forever).
func TestInitManifestCorruptSelfHeals(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".manifest.json"), []byte(""), 0o644) // 0-byte interrupt
	if err := InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".manifest.json"))
	if len(data) == 0 {
		t.Error("0-byte manifest was preserved — re-init must self-heal")
	}
}

// Review: vault overlay also ignores .sage/ (git-tracked vault).
func TestInitVaultOverlayWritesGitignore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "notes"), 0o755)
	if err := InitVaultOverlay(dir, "test", []string{"notes"}, nil, "wiki", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil || string(data) != ".sage/\n" {
		t.Errorf("vault overlay must write .gitignore with .sage/, got %q (err %v)", data, err)
	}
}

// Review: leading-space ".sage/" does NOT count as ignored (git strips
// trailing, not leading whitespace); ".sage" (no slash) DOES count.
func TestInitGitignoreLeadingSpace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(" .sage/\n"), 0o644)
	if err := InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var exact int
	for _, l := range lines {
		if strings.TrimRight(l, " \t\r") == ".sage/" || strings.TrimRight(l, " \t\r") == ".sage" {
			exact++
		}
	}
	if exact != 2 {
		t.Errorf("leading-space entry must earn a real .sage/ append (2 matching lines), got %d: %q", exact, data)
	}
}
