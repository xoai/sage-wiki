package linter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupLintProject(t *testing.T) (string, *LintContext) {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")

	ctx := &LintContext{
		ProjectDir:     dir,
		OutputDir:      "wiki",
		DBPath:         filepath.Join(dir, ".sage", "wiki.db"),
		ValidRelations: ontology.ValidRelationNames(ontology.BuiltinRelations),
	}
	return dir, ctx
}

func TestCompletenessPass(t *testing.T) {
	dir, ctx := setupLintProject(t)

	// Create an article with a broken link
	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "attention.md"), []byte(`---
concept: attention
---

# Attention

See also: [[nonexistent-concept]] and [[kv-cache]]
`), 0644)

	pass := &CompletenessPass{}
	findings, err := pass.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should find broken links
	found := false
	for _, f := range findings {
		if f.Message == `broken [[nonexistent-concept]] — no article exists` {
			found = true
		}
	}
	if !found {
		t.Error("expected finding for broken [[nonexistent-concept]] link")
	}
}

func TestStylePass(t *testing.T) {
	dir, ctx := setupLintProject(t)

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)

	// Article without frontmatter
	os.WriteFile(filepath.Join(conceptsDir, "no-frontmatter.md"), []byte("# No Frontmatter\nContent."), 0644)
	// Article with frontmatter
	os.WriteFile(filepath.Join(conceptsDir, "with-frontmatter.md"), []byte("---\nconcept: test\n---\n\n# Test"), 0644)

	pass := &StylePass{}
	findings, _ := pass.Run(ctx)

	hasNoFM := false
	for _, f := range findings {
		if f.Path == "wiki/concepts/no-frontmatter.md" && f.Fix == "add frontmatter" {
			hasNoFM = true
		}
	}
	if !hasNoFM {
		t.Error("expected finding for missing frontmatter")
	}
}

func TestStylePassAutoFix(t *testing.T) {
	dir, ctx := setupLintProject(t)

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "fix-me.md"), []byte("# Fix Me\nContent."), 0644)

	pass := &StylePass{}
	findings, _ := pass.Run(ctx)
	pass.Fix(ctx, findings)

	// Verify frontmatter was added
	data, _ := os.ReadFile(filepath.Join(conceptsDir, "fix-me.md"))
	if string(data[:3]) != "---" {
		t.Error("expected frontmatter to be added")
	}
}

func TestImputePass(t *testing.T) {
	dir, ctx := setupLintProject(t)

	conceptsDir := filepath.Join(dir, "wiki", "concepts")
	os.MkdirAll(conceptsDir, 0755)
	os.WriteFile(filepath.Join(conceptsDir, "todo-article.md"), []byte(`---
concept: todo
---

## Definition
[TODO] needs content

## How it works
x
`), 0644)

	pass := &ImputePass{}
	findings, _ := pass.Run(ctx)

	hasTodo := false
	hasThin := false
	for _, f := range findings {
		if f.Pass == "impute" && f.Severity == SevWarning {
			hasTodo = true
		}
		if f.Pass == "impute" && f.Severity == SevInfo {
			hasThin = true
		}
	}
	if !hasTodo {
		t.Error("expected finding for [TODO] placeholder")
	}
	if !hasThin {
		t.Error("expected finding for thin section")
	}
}

func TestRunnerAllPasses(t *testing.T) {
	_, ctx := setupLintProject(t)

	runner := NewRunner()
	results, err := runner.Run(ctx, "", false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have results for all passes (even if empty)
	if len(results) != 7 {
		t.Errorf("expected 7 pass results, got %d", len(results))
	}
}

func TestRunnerSpecificPass(t *testing.T) {
	_, ctx := setupLintProject(t)

	runner := NewRunner()
	results, _ := runner.Run(ctx, "style", false)

	if len(results) != 1 {
		t.Errorf("expected 1 pass result, got %d", len(results))
	}
	if results[0].PassName != "style" {
		t.Errorf("expected style pass, got %s", results[0].PassName)
	}
}

func TestSaveReport(t *testing.T) {
	dir, _ := setupLintProject(t)

	results := []LintResult{
		{PassName: "test", Findings: []Finding{{Pass: "test", Severity: SevWarning, Message: "test finding"}}},
	}

	if err := SaveReport(dir, results); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	// Verify files created
	entries, _ := os.ReadDir(filepath.Join(dir, ".sage", "lintlog"))
	if len(entries) < 2 {
		t.Errorf("expected at least 2 report files (json + txt), got %d", len(entries))
	}
}

func TestStoreLearning(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, _ := storage.Open(dbPath)
	defer db.Close()

	// Store
	err := StoreLearning(db, "gotcha", "test learning content", "tag1,tag2", "consistency")
	if err != nil {
		t.Fatalf("StoreLearning: %v", err)
	}

	// Dedup — same content should not duplicate
	StoreLearning(db, "gotcha", "test learning content", "tag1,tag2", "consistency")

	learnings, _ := ListLearnings(db)
	if len(learnings) != 1 {
		t.Errorf("expected 1 learning (dedup), got %d", len(learnings))
	}
}

func TestPruneLearnings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, _ := storage.Open(dbPath)
	defer db.Close()

	// Store enough to exceed limit
	for i := 0; i < 10; i++ {
		StoreLearning(db, "gotcha", "learning content "+string(rune('A'+i)), "", "test")
	}

	pruned, err := PruneLearnings(db)
	if err != nil {
		t.Fatalf("PruneLearnings: %v", err)
	}

	// Should not prune anything (under 500 limit)
	if pruned != 0 {
		t.Errorf("expected 0 pruned (under limit), got %d", pruned)
	}
}

func TestRecallLearnings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, _ := storage.Open(dbPath)
	defer db.Close()

	StoreLearning(db, "gotcha", "attention memory vs IO bandwidth", "attention,memory", "consistency")
	StoreLearning(db, "convention", "use snake_case for entity IDs", "naming", "style")

	results, _ := RecallLearnings(db, "attention", 5)
	if len(results) != 1 {
		t.Errorf("expected 1 matching learning, got %d", len(results))
	}
}
