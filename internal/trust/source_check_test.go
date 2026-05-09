package trust

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckSourceChangesTriggeredByModifiedFile(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "wiki", "concepts")
	os.MkdirAll(srcDir, 0755)
	srcPath := filepath.Join(srcDir, "x.md")
	os.WriteFile(srcPath, []byte("original content"), 0644)

	sourcesJSON := `["wiki/concepts/x.md"]`
	originalHash := ComputeSourcesHash(projectDir, sourcesJSON)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 1,
		SourcesHash: originalHash, SourcesUsed: sourcesJSON,
		FilePath: "wiki/outputs/test.md", CreatedAt: time.Now(),
	})
	store.Promote("test.md")

	os.WriteFile(srcPath, []byte("modified content"), 0644)

	demoted, err := CheckSourceChanges(store, projectDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if demoted != 1 {
		t.Errorf("demoted = %d, want 1", demoted)
	}

	got, _ := store.Get("test.md")
	if got.State != StateStale {
		t.Errorf("state = %q, want stale", got.State)
	}
}

func TestCheckSourceChangesNoChangeStaysConfirmed(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "wiki", "concepts")
	os.MkdirAll(srcDir, 0755)
	srcPath := filepath.Join(srcDir, "x.md")
	os.WriteFile(srcPath, []byte("stable content"), 0644)

	sourcesJSON := `["wiki/concepts/x.md"]`
	hash := ComputeSourcesHash(projectDir, sourcesJSON)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 1,
		SourcesHash: hash, SourcesUsed: sourcesJSON,
		FilePath: "wiki/outputs/test.md", CreatedAt: time.Now(),
	})
	store.Promote("test.md")

	demoted, err := CheckSourceChanges(store, projectDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if demoted != 0 {
		t.Errorf("demoted = %d, want 0", demoted)
	}

	got, _ := store.Get("test.md")
	if got.State != StateConfirmed {
		t.Errorf("state = %q, want confirmed", got.State)
	}
}
