package trust

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/vectors"
)

func TestPromoteOutputIndexes(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil, nil)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is X?", QuestionHash: HashQuestion("What is X?"),
		Answer: "X is a thing.", AnswerHash: HashAnswer("X is a thing."),
		State: StatePending, Confirmations: 1,
		SourcesUsed: `["wiki/concepts/x.md"]`,
		FilePath:    "wiki/outputs/test.md", CreatedAt: time.Now(),
	})

	stores := IndexStores{
		MemStore: memStore,
		VecStore: vecStore,
		OntStore: ontStore,
	}

	if err := PromoteOutput(store, "test.md", t.TempDir(), stores); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Get("test.md")
	if got.State != StateConfirmed {
		t.Errorf("state = %q, want confirmed", got.State)
	}

	entry, err := memStore.Get("output:test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry == nil {
		t.Fatal("expected FTS5 entry after promote, got nil")
	}
	if entry.Content != "X is a thing." {
		t.Errorf("FTS5 content = %q", entry.Content)
	}

	entity, err := ontStore.GetEntity("output:test.md")
	if err != nil {
		t.Errorf("expected ontology entity after promote: %v", err)
	}
	if entity.Name != "What is X?" {
		t.Errorf("entity name = %q", entity.Name)
	}
}

func TestDemoteOutputDeindexes(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil, nil)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is X?", QuestionHash: HashQuestion("What is X?"),
		Answer: "X is a thing.", AnswerHash: HashAnswer("X is a thing."),
		State: StatePending, Confirmations: 1,
		FilePath: "wiki/outputs/test.md", CreatedAt: time.Now(),
	})

	stores := IndexStores{
		MemStore: memStore,
		VecStore: vecStore,
		OntStore: ontStore,
	}

	PromoteOutput(store, "test.md", t.TempDir(), stores)

	if err := DemoteOutput(store, "test.md", stores); err != nil {
		t.Fatal(err)
	}

	got, _ := store.Get("test.md")
	if got.State != StateStale {
		t.Errorf("state = %q, want stale", got.State)
	}

	entry, err := memStore.Get("output:test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry != nil {
		t.Error("expected FTS5 entry to be removed after demote")
	}
}
