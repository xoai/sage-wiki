package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateExistingOutputs(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	outputsDir := filepath.Join(projectDir, "wiki", "outputs")
	os.MkdirAll(outputsDir, 0755)

	content := `---
question: "What is X?"
sources: [wiki/concepts/x.md]
created_at: 2026-05-09
format: markdown
---

X is a thing created by Acme Corp.
`
	os.WriteFile(filepath.Join(outputsDir, "2026-05-09-what-is-x.md"), []byte(content), 0644)

	var deindexed []string
	deindex := func(id string) {
		deindexed = append(deindexed, id)
	}

	result, err := MigrateExistingOutputs(store, projectDir, "wiki", deindex)
	if err != nil {
		t.Fatal(err)
	}
	if result.Migrated != 1 {
		t.Errorf("migrated = %d, want 1", result.Migrated)
	}
	if result.Skipped != 0 {
		t.Errorf("skipped = %d, want 0", result.Skipped)
	}

	got, err := store.Get("2026-05-09-what-is-x.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Question != "What is X?" {
		t.Errorf("question = %q", got.Question)
	}
	if got.State != StatePending {
		t.Errorf("state = %q", got.State)
	}

	if len(deindexed) != 1 || deindexed[0] != "2026-05-09-what-is-x.md" {
		t.Errorf("deindexed = %v", deindexed)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	outputsDir := filepath.Join(projectDir, "wiki", "outputs")
	os.MkdirAll(outputsDir, 0755)

	content := `---
question: "What is Y?"
---

Y is a thing.
`
	os.WriteFile(filepath.Join(outputsDir, "test.md"), []byte(content), 0644)

	MigrateExistingOutputs(store, projectDir, "wiki", nil)

	result, err := MigrateExistingOutputs(store, projectDir, "wiki", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Migrated != 0 {
		t.Errorf("migrated = %d on second run, want 0", result.Migrated)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", result.Skipped)
	}
}

func TestMigrateNoOutputsDir(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	result, err := MigrateExistingOutputs(store, t.TempDir(), "wiki", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Migrated != 0 {
		t.Errorf("migrated = %d, want 0", result.Migrated)
	}
}
