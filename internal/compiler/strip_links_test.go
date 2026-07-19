package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
)

// TestStripBrokenWikilinks_ReindexesFTS verifies the strip keeps FTS consistent
// with the rewritten article file (I2), so the startup reconciler does not later
// re-embed the stripped article to heal a self-inflicted drift.
func TestStripBrokenWikilinks_ReindexesFTS(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	mem := memory.NewStore(db)

	dir := t.TempDir()
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// "real" exists; "ghost" does not → the link to ghost is dead.
	os.WriteFile(filepath.Join(conceptsDir, "real.md"), []byte("# Real\n"), 0644)
	preStrip := "# Attention\n\nSee [[real]] and [[ghost]].\n"
	os.WriteFile(filepath.Join(conceptsDir, "attention.md"), []byte(preStrip), 0644)

	// Index the article into FTS with its PRE-strip content, as Pass 3 does.
	if err := mem.Add(memory.Entry{ID: "concept:attention", Content: preStrip, ArticlePath: "wiki/concepts/attention.md", Tags: []string{"concept"}}); err != nil {
		t.Fatalf("seed FTS: %v", err)
	}

	if _, err := StripBrokenWikilinks(dir, "wiki", mem); err != nil {
		t.Fatalf("strip: %v", err)
	}

	// The file no longer contains [[ghost]]...
	fileNow, _ := os.ReadFile(filepath.Join(conceptsDir, "attention.md"))
	if strings.Contains(string(fileNow), "[[ghost]]") {
		t.Fatal("dead link not stripped from file")
	}
	// ...and the FTS entry was updated to match the file (no drift for the
	// reconciler to heal).
	entry, err := mem.Get("concept:attention")
	if err != nil || entry == nil {
		t.Fatalf("FTS entry missing: %v", err)
	}
	if entry.Content != string(fileNow) {
		t.Errorf("FTS content not re-indexed after strip:\n file=%q\n  fts=%q", fileNow, entry.Content)
	}
	if strings.Contains(entry.Content, "[[ghost]]") {
		t.Error("FTS still holds the pre-strip dead link")
	}
	// The link to the existing "real" article is preserved.
	if !strings.Contains(entry.Content, "[[real]]") {
		t.Error("valid link to existing article was wrongly stripped")
	}
}

// TestStripBrokenWikilinks verifies the post-compile sweep strips [[wikilinks]]
// pointing at concept articles that don't exist on disk, while leaving links
// to existing articles intact. Issue #90.
func TestStripBrokenWikilinks(t *testing.T) {
	dir := t.TempDir()
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptsDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Three concept articles. "flash-attention" and "self-attention" exist;
	// "groundwater-contamination" does not.
	mustWrite := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(conceptsDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("flash-attention.md", `# Flash Attention
Improves on [[self-attention]]. Cited in [[groundwater-contamination]] (phantom).
Mentions [[soil-remediation]] which also doesn't exist.
`)
	mustWrite("self-attention.md", `# Self-Attention
Baseline for [[flash-attention]] comparisons. No phantoms here.
`)

	stats, err := StripBrokenWikilinks(dir, "wiki", nil)
	if err != nil {
		t.Fatalf("StripBrokenWikilinks: %v", err)
	}
	if stats.ArticlesScanned != 2 {
		t.Errorf("ArticlesScanned = %d, want 2", stats.ArticlesScanned)
	}
	if stats.ArticlesEdited != 1 {
		t.Errorf("ArticlesEdited = %d, want 1 (only flash-attention.md had phantoms)", stats.ArticlesEdited)
	}
	if stats.LinksStripped != 2 {
		t.Errorf("LinksStripped = %d, want 2", stats.LinksStripped)
	}

	// Verify flash-attention.md content: existing links preserved, phantoms stripped.
	got, _ := os.ReadFile(filepath.Join(conceptsDir, "flash-attention.md"))
	s := string(got)
	if !strings.Contains(s, "[[self-attention]]") {
		t.Error("[[self-attention]] should be preserved (article exists)")
	}
	if strings.Contains(s, "[[groundwater-contamination]]") {
		t.Error("[[groundwater-contamination]] should have been stripped")
	}
	if !strings.Contains(s, "groundwater-contamination") {
		t.Error("groundwater-contamination text should remain (just without brackets)")
	}
	if strings.Contains(s, "[[soil-remediation]]") {
		t.Error("[[soil-remediation]] should have been stripped")
	}

	// self-attention.md should be untouched
	got, _ = os.ReadFile(filepath.Join(conceptsDir, "self-attention.md"))
	if !strings.Contains(string(got), "[[flash-attention]]") {
		t.Error("self-attention.md should retain its [[flash-attention]] link")
	}
}

// TestStripBrokenWikilinks_NoConceptsDir is a no-op when the dir is absent
// (e.g., a project freshly initialized with no compile run yet).
func TestStripBrokenWikilinks_NoConceptsDir(t *testing.T) {
	dir := t.TempDir()
	stats, err := StripBrokenWikilinks(dir, "wiki", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.ArticlesScanned != 0 {
		t.Errorf("ArticlesScanned = %d, want 0", stats.ArticlesScanned)
	}
}

// TestStripBrokenWikilinks_IdempotentOnSecondRun verifies a second sweep is
// a no-op once the first has stripped everything strippable.
func TestStripBrokenWikilinks_IdempotentOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "x.md"), []byte("[[phantom]]"), 0644)

	if _, err := StripBrokenWikilinks(dir, "wiki", nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stats, err := StripBrokenWikilinks(dir, "wiki", nil)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if stats.LinksStripped != 0 || stats.ArticlesEdited != 0 {
		t.Errorf("second run should be no-op; got stripped=%d edited=%d", stats.LinksStripped, stats.ArticlesEdited)
	}
}

// TestMaybeStripBrokenWikilinks_DisabledByConfig verifies the helper short-
// circuits when the config flag is off, leaving phantom links in place.
// This is the opt-out path users need when they want to keep broken links
// as a worklist for future compiles.
func TestMaybeStripBrokenWikilinks_DisabledByConfig(t *testing.T) {
	dir := t.TempDir()
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "x.md"), []byte("[[phantom]]"), 0644)

	MaybeStripBrokenWikilinks(dir, "wiki", false, nil)

	got, _ := os.ReadFile(filepath.Join(conceptsDir, "x.md"))
	if string(got) != "[[phantom]]" {
		t.Errorf("disabled helper should not modify articles; got %q", string(got))
	}
}

// TestMaybeStripBrokenWikilinks_RunsWhenEnabled verifies the wrapper actually
// invokes the strip when enabled. Smoke test for the wiring used by all
// post-Pass-3 call sites (Compile, ReExtract — issue #94).
func TestMaybeStripBrokenWikilinks_RunsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "x.md"), []byte("[[phantom]] and bare text"), 0644)

	MaybeStripBrokenWikilinks(dir, "wiki", true, nil)

	got, _ := os.ReadFile(filepath.Join(conceptsDir, "x.md"))
	if strings.Contains(string(got), "[[phantom]]") {
		t.Errorf("enabled helper should have stripped [[phantom]]; got %q", string(got))
	}
	if !strings.Contains(string(got), "phantom") {
		t.Errorf("phantom text should remain (just without brackets); got %q", string(got))
	}
}
