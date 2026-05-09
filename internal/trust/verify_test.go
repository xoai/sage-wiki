package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyAutoPromoteAboveThreshold(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "wiki", "concepts")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "x.md"), []byte("X is a concept created by Acme Corp."), 0644)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is X?", QuestionHash: HashQuestion("What is X?"),
		Answer: "X was created by Acme Corp.", AnswerHash: HashAnswer("X was created by Acme Corp."),
		State: StatePending, Confirmations: 2, SourcesUsed: `["wiki/concepts/x.md"]`,
		FilePath: "wiki/outputs/test.md", CreatedAt: time.Now(),
	})
	// Independent confirmations with disjoint chunks → independence > 0.3
	store.RecordConfirmation("test.md", `["chunk-a"]`, HashAnswer("X was created by Acme Corp."))
	store.RecordConfirmation("test.md", `["chunk-b"]`, HashAnswer("X was created by Acme Corp."))

	callCount := 0
	client, cleanup := mockLLMServer(t, func(body string) string {
		callCount++
		if callCount == 1 {
			return `[{"text": "X was created by Acme Corp."}]`
		}
		return "grounded"
	})
	defer cleanup()

	results, err := Verify(store, client, "test-model", projectDir, VerifyOpts{
		All: true, AutoPromote: true, Threshold: 0.7, ConsensusThreshold: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != nil {
		t.Fatal(results[0].Error)
	}
	if results[0].GroundingScore != 1.0 {
		t.Errorf("score = %v, want 1.0", results[0].GroundingScore)
	}
	if !results[0].Promoted {
		t.Error("expected output to be promoted")
	}

	got, _ := store.Get("test.md")
	if got.State != StateConfirmed {
		t.Errorf("state = %q, want confirmed", got.State)
	}
}

func TestVerifyKeepPendingBelowThreshold(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "wiki", "concepts")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "x.md"), []byte("X is something else entirely."), 0644)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is X?", QuestionHash: HashQuestion("What is X?"),
		Answer: "X was created by Acme Corp.", AnswerHash: HashAnswer("X was created by Acme Corp."),
		State: StatePending, Confirmations: 1, SourcesUsed: `["wiki/concepts/x.md"]`,
		FilePath: "wiki/outputs/test.md", CreatedAt: time.Now(),
	})

	callCount := 0
	client, cleanup := mockLLMServer(t, func(body string) string {
		callCount++
		if callCount == 1 {
			return `[{"text": "X was created by Acme Corp."}]`
		}
		return "ungrounded"
	})
	defer cleanup()

	results, err := Verify(store, client, "test-model", projectDir, VerifyOpts{
		All: true, AutoPromote: true, Threshold: 0.7, ConsensusThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Promoted {
		t.Error("should not be promoted below threshold")
	}

	got, _ := store.Get("test.md")
	if got.State != StatePending {
		t.Errorf("state = %q, want pending", got.State)
	}
}

func TestVerifyLimit(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()

	for i := 0; i < 5; i++ {
		store.InsertPending(&PendingOutput{
			ID: strings.Replace("test-#.md", "#", string(rune('a'+i)), 1),
			Question: "Q", QuestionHash: "h", Answer: "A", AnswerHash: "ah",
			State: StatePending, Confirmations: 1, SourcesUsed: `[]`,
			FilePath: "p", CreatedAt: time.Now(),
		})
	}

	client, cleanup := mockLLMServer(t, func(body string) string {
		return `[]`
	})
	defer cleanup()

	results, err := Verify(store, client, "test-model", projectDir, VerifyOpts{
		All: true, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (limit), got %d", len(results))
	}
}

func TestVerifySinceFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	projectDir := t.TempDir()

	store.InsertPending(&PendingOutput{
		ID: "old.md", Question: "Q", QuestionHash: "h", Answer: "A", AnswerHash: "ah",
		State: StatePending, Confirmations: 1, SourcesUsed: `[]`,
		FilePath: "p", CreatedAt: time.Now().Add(-48 * time.Hour),
	})
	store.InsertPending(&PendingOutput{
		ID: "new.md", Question: "Q2", QuestionHash: "h2", Answer: "A2", AnswerHash: "ah2",
		State: StatePending, Confirmations: 1, SourcesUsed: `[]`,
		FilePath: "p2", CreatedAt: time.Now(),
	})

	client, cleanup := mockLLMServer(t, func(body string) string {
		return `[]`
	})
	defer cleanup()

	results, err := Verify(store, client, "test-model", projectDir, VerifyOpts{
		Since: 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (since filter), got %d", len(results))
	}
}
