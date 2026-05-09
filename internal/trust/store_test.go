package trust

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) *storage.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStoreInsertAndGet(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	o := &PendingOutput{
		ID:           "2026-05-09-test.md",
		Question:     "What is X?",
		QuestionHash: HashQuestion("What is X?"),
		Answer:       "X is a thing.",
		AnswerHash:   HashAnswer("X is a thing."),
		State:        StatePending,
		Confirmations: 1,
		SourcesHash:  "abc123",
		SourcesUsed:  `["wiki/concepts/x.md"]`,
		FilePath:     "wiki/outputs/2026-05-09-test.md",
		CreatedAt:    time.Now(),
	}

	if err := store.InsertPending(o); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get("2026-05-09-test.md")
	if err != nil {
		t.Fatal(err)
	}
	if got.Question != "What is X?" {
		t.Errorf("Question = %q", got.Question)
	}
	if got.State != StatePending {
		t.Errorf("State = %q", got.State)
	}
	if got.Confirmations != 1 {
		t.Errorf("Confirmations = %d", got.Confirmations)
	}
}

func TestStoreListByState(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "a.md", Question: "Q1", QuestionHash: "h1", Answer: "A1",
		AnswerHash: "ah1", State: StatePending, Confirmations: 1,
		FilePath: "p1", CreatedAt: time.Now(),
	})
	store.InsertPending(&PendingOutput{
		ID: "b.md", Question: "Q2", QuestionHash: "h2", Answer: "A2",
		AnswerHash: "ah2", State: StatePending, Confirmations: 1,
		FilePath: "p2", CreatedAt: time.Now(),
	})

	pending, err := store.ListByState(StatePending)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending, got %d", len(pending))
	}

	confirmed, _ := store.ListByState(StateConfirmed)
	if len(confirmed) != 0 {
		t.Errorf("expected 0 confirmed, got %d", len(confirmed))
	}
}

func TestStorePromoteAndDemote(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 3,
		FilePath: "p", CreatedAt: time.Now(),
	})

	if err := store.Promote("test.md"); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Get("test.md")
	if got.State != StateConfirmed {
		t.Errorf("State = %q, want confirmed", got.State)
	}
	if got.PromotedAt == nil {
		t.Error("PromotedAt should be set")
	}
	if !store.IsConfirmed("test.md") {
		t.Error("IsConfirmed should return true")
	}

	if err := store.Demote("test.md"); err != nil {
		t.Fatal(err)
	}

	got, _ = store.Get("test.md")
	if got.State != StateStale {
		t.Errorf("State = %q, want stale", got.State)
	}
	if got.Confirmations != 0 {
		t.Errorf("Confirmations = %d, want 0 after demotion", got.Confirmations)
	}
	if got.GroundingScore != nil {
		t.Error("GroundingScore should be nil after demotion")
	}
}

func TestStoreDelete(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "del.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 1,
		FilePath: "p", CreatedAt: time.Now(),
	})
	store.RecordConfirmation("del.md", `["c1","c2"]`, "ah")

	if err := store.Delete("del.md"); err != nil {
		t.Fatal(err)
	}

	_, err := store.Get("del.md")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestStoreConfirmations(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "conf.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 1,
		FilePath: "p", CreatedAt: time.Now(),
	})

	store.RecordConfirmation("conf.md", `["chunk1"]`, "ah")
	store.RecordConfirmation("conf.md", `["chunk2","chunk3"]`, "ah")

	confs, err := store.GetConfirmations("conf.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(confs) != 2 {
		t.Errorf("expected 2 confirmations, got %d", len(confs))
	}
}

func TestStoreGroundingScore(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "gs.md", Question: "Q", QuestionHash: "h", Answer: "A",
		AnswerHash: "ah", State: StatePending, Confirmations: 1,
		FilePath: "p", CreatedAt: time.Now(),
	})

	store.UpdateGroundingScore("gs.md", 0.85)

	got, _ := store.Get("gs.md")
	if got.GroundingScore == nil || *got.GroundingScore != 0.85 {
		t.Errorf("GroundingScore = %v, want 0.85", got.GroundingScore)
	}
}

func TestHashFunctions(t *testing.T) {
	h1 := HashQuestion("What is X?")
	h2 := HashQuestion("What is X?")
	h3 := HashQuestion("What is Y?")

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 32 {
		t.Errorf("hash length = %d, want 32", len(h1))
	}
}
