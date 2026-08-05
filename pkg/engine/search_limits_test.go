package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
)

// SPEC-08 Task 10: max_query_bytes at the engine boundary + the graph
// traversal node cap (AC12).

func TestSearchQueryTooLarge(t *testing.T) {
	dir := initWorkspace(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_query_bytes: 10\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	_, err = w.Search(context.Background(), SearchRequest{Query: strings.Repeat("q", 20)})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if !errors.Is(err, limits.ErrQueryTooLarge) {
		t.Error("errors.Is(err, ErrQueryTooLarge) = false")
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Errorf("limit_exceeded events = %d, want 1", len(got))
	}
}

func TestSearchQueryUnderLimitWorks(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Search(context.Background(), SearchRequest{Query: "hello"}); err != nil {
		t.Fatalf("benign search: %v", err)
	}
}

// chainWorkspace seeds a linear entity chain of n entities (e0-e1-...-e{n-1}).
func chainWorkspace(t *testing.T, w *Workspace, n int) {
	t.Helper()
	ont := w.app.Ont
	for i := 0; i < n; i++ {
		id := "e-" + string(rune('a'+i))
		if err := ont.AddEntity(store.Entity{ID: id, Type: "concept", Name: id, CreatedAt: "2026-01-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i+1 < n; i++ {
		from := "e-" + string(rune('a'+i))
		to := "e-" + string(rune('a'+i+1))
		if err := ont.AddRelation(store.Relation{
			ID: "r" + string(rune('a'+i)), SourceID: from, TargetID: to,
			Relation: ontology.RelCites, CreatedAt: "2026-01-02T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGraphTraversalCapPartialResult(t *testing.T) {
	dir := initWorkspace(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// Cap the traversal at 3 visited nodes (start + 2 neighbors).
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_graph_traversal_nodes: 3\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &captureSink{}
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chainWorkspace(t, w, 6)

	ents, err := w.Graph().Neighbors(context.Background(), "e-a", 5)
	if !errors.Is(err, limits.ErrTraversalTooWide) {
		t.Fatalf("err = %v, want ErrTraversalTooWide", err)
	}
	if len(ents) == 0 {
		t.Fatal("partial result must not be empty")
	}
	if len(ents) >= 5 {
		t.Errorf("partial result returned %d entities, want a truncated set", len(ents))
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Errorf("limit_exceeded events = %d, want 1", len(got))
	}
}

func TestGraphTraversalUnderCapComplete(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	chainWorkspace(t, w, 4)

	ents, err := w.Graph().Neighbors(context.Background(), "e-a", 5)
	if err != nil {
		t.Fatalf("under-cap traversal errored: %v", err)
	}
	if len(ents) != 3 {
		t.Errorf("neighbors = %d, want 3 (complete chain minus start)", len(ents))
	}
}
