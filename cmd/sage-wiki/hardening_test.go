package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/traversaltest"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// SPEC-08 AC1: CLI ingestion surfaces reject the shared traversal table
// with typed errors.

func greenfieldCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "hardening", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	old := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = old })
	return dir
}

func TestAddSourceRejectsTraversal(t *testing.T) {
	dir := greenfieldCLI(t)
	for _, tc := range traversaltest.Cases() {
		err := runAddSource(addSourceCmd, []string{tc.Input})
		if err == nil {
			t.Errorf("%s: add-source %q succeeded, want typed rejection", tc.Name, tc.Input)
			continue
		}
		switch tc.Family {
		case "traversal":
			if !errors.Is(err, limits.ErrTraversalTooWide) {
				t.Errorf("%s: err = %v, want ErrTraversalTooWide", tc.Name, err)
			}
		case "malformed":
			if !errors.Is(err, limits.ErrInvalidName) {
				t.Errorf("%s: err = %v, want ErrInvalidName", tc.Name, err)
			}
		}
	}
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mf.SourceCount() != 0 {
		t.Errorf("rejected add-source registered %d sources", mf.SourceCount())
	}
}

func TestAddSourceBenignWorks(t *testing.T) {
	dir := greenfieldCLI(t)
	if err := os.WriteFile(filepath.Join(dir, "raw", "note.md"), []byte("# Note"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runAddSource(addSourceCmd, []string{"raw/note.md"}); err != nil {
		t.Fatalf("benign add-source: %v", err)
	}
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mf.SourceCount() != 1 {
		t.Errorf("source count = %d, want 1", mf.SourceCount())
	}
}

func TestWriteArticleRejectsBadConcept(t *testing.T) {
	dir := greenfieldCLI(t)
	for _, bad := range []string{"../../evil", "Has_Upper", "..", "a/b", ""} {
		writeArticleCmd.Flags().Set("concept", bad)
		writeArticleCmd.Flags().Set("content", "body")
		err := runWriteArticle(writeArticleCmd, nil)
		if err == nil {
			t.Errorf("write article --concept %q succeeded, want charset rejection", bad)
			continue
		}
		if !errors.Is(err, limits.ErrInvalidName) {
			t.Errorf("concept %q: err = %v, want ErrInvalidName", bad, err)
		}
	}
	// Nothing may land outside the concepts dir.
	if _, err := os.Stat(filepath.Join(dir, "evil.md")); !os.IsNotExist(err) {
		t.Error("article escaped the concepts dir")
	}
}

func TestWriteArticleBenignConceptWorks(t *testing.T) {
	greenfieldCLI(t)
	writeArticleCmd.Flags().Set("concept", "self-attention")
	writeArticleCmd.Flags().Set("content", "# Self Attention\nbody")
	if err := runWriteArticle(writeArticleCmd, nil); err != nil {
		t.Fatalf("benign write article: %v", err)
	}
}
