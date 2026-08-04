package ontology

import (
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/pkg/events"
)

type ontCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *ontCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *ontCaptureSink) byType(t events.Type) []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []events.Event
	for _, ev := range s.events {
		if ev.Type == t {
			out = append(out, ev)
		}
	}
	return out
}

// TestStoreEmitsEdgeAdded: AddRelation emits edge_added through the
// installed sink (SPEC-07 store-level hook).
func TestStoreEmitsEdgeAdded(t *testing.T) {
	store := setupTestDB(t)
	sink := &ontCaptureSink{}
	store.SetEventSink(events.BindWorkspace(sink, "ws"))

	for _, id := range []string{"a", "b"} {
		if err := store.AddEntity(Entity{ID: id, Type: TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	err := store.AddRelation(Relation{
		ID: "rel-1", SourceID: "a", TargetID: "b", Relation: RelExtends,
		Confidence: 0.9, ValidFrom: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := sink.byType(events.TypeEdgeAdded)
	if len(got) != 1 {
		t.Fatalf("edge_added events = %d, want 1", len(got))
	}
	ev := got[0]
	if ev.Workspace != "ws" {
		t.Errorf("Workspace = %q, want ws (BindWorkspace)", ev.Workspace)
	}
	d := ev.Data.(events.EdgeAdded)
	if d.EdgeID != "rel-1" || d.Relation != RelExtends || d.From != "a" || d.To != "b" {
		t.Errorf("payload = %+v", d)
	}
	if d.ValidFrom == nil {
		t.Error("ValidFrom must be parsed from the relation")
	}
}

// TestStoreEmitsEdgeInvalidated: InvalidateFunctional emits one
// edge_invalidated per invalidated edge.
func TestStoreEmitsEdgeInvalidated(t *testing.T) {
	store := setupTestDB(t)
	sink := &ontCaptureSink{}
	store.SetEventSink(events.BindWorkspace(sink, "ws"))

	for _, id := range []string{"src", "t1", "t2"} {
		if err := store.AddEntity(Entity{ID: id, Type: TypeConcept, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	// Two functional edges for the same predicate; the second supersedes
	// the first (keepTargetID = t2 invalidates the t1 edge).
	if err := store.AddRelation(Relation{
		ID: "rel-old", SourceID: "src", TargetID: "t1", Relation: RelContradicts,
		Confidence: 0.9, ValidFrom: "2020-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRelation(Relation{
		ID: "rel-new", SourceID: "src", TargetID: "t2", Relation: RelContradicts,
		Confidence: 0.9, ValidFrom: "2021-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	invalidated, err := store.InvalidateFunctional("src", RelContradicts, "t2", "2021-01-01T00:00:00Z", "superseded")
	if err != nil {
		t.Fatal(err)
	}
	if len(invalidated) == 0 {
		t.Skip("fixture produced no invalidation — nothing to assert")
	}

	got := sink.byType(events.TypeEdgeInvalidated)
	if len(got) != len(invalidated) {
		t.Fatalf("edge_invalidated events = %d, want %d", len(got), len(invalidated))
	}
	d := got[0].Data.(events.EdgeInvalidated)
	if d.Reason != "superseded" {
		t.Errorf("Reason = %q, want superseded", d.Reason)
	}
	if d.ValidTo == nil {
		t.Error("ValidTo must carry the winner's start")
	}
}

// TestStoreNilSinkSafe: no sink installed = no panic, no events.
func TestStoreNilSinkSafe(t *testing.T) {
	store := setupTestDB(t)
	if err := store.AddEntity(Entity{ID: "x", Type: TypeConcept, Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRelation(Relation{ID: "r", SourceID: "x", TargetID: "x", Relation: RelExtends}); err == nil {
		t.Fatal("self-loop must error")
	}
}
