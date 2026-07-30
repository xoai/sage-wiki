package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #127: `init <dir>` must initialize the named directory, not the cwd.
func TestInitPositionalArgHonored(t *testing.T) {
	target := filepath.Join(t.TempDir(), "named")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	old := projectDir
	t.Cleanup(func() { projectDir = old })
	if err := runInit(initCmd, []string{target}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "config.yaml")); err != nil {
		t.Errorf("config.yaml not created in named dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".manifest.json")); err != nil {
		t.Errorf("manifest not created in named dir: %v", err)
	}
}

// More than one positional arg must error loudly (MaximumNArgs).
func TestInitTooManyArgs(t *testing.T) {
	if err := initCmd.Args(initCmd, []string{"a", "b"}); err == nil {
		t.Error("init a b must error, got nil")
	}
}

// Re-init preserves user files; --force rewrites them.
func TestInitForceFlag(t *testing.T) {
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, ".manifest.json"),
		[]byte(`{"version":2,"sources":{"raw/a.md":{}},"concepts":{"alpha":{}}}`+"\n"), 0o644)

	old := projectDir
	t.Cleanup(func() { projectDir = old })
	projectDir = target
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(target, ".manifest.json"))
	if !strings.Contains(string(data), "alpha") {
		t.Error("re-init without --force destroyed the manifest")
	}

	initCmd.Flags().Set("force", "true")
	t.Cleanup(func() { initCmd.Flags().Set("force", "false") })
	if err := runInit(initCmd, nil); err != nil {
		t.Fatalf("runInit --force: %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(target, ".manifest.json"))
	if strings.Contains(string(data), "alpha") {
		t.Error("--force must rewrite the manifest")
	}
}
