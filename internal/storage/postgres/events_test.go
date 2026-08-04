package postgres

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
)

type pgCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *pgCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *pgCaptureSink) count(t events.Type) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

// TestOntologyStoreEmitsEdgeEvents: the postgres twin carries the same
// SPEC-07 seam as the sqlite store — SetEventSink + edge_added emission.
// Env-gated: runs in the CI postgres job (TEST_DATABASE_URL), skipped
// offline.
func TestOntologyStoreEmitsEdgeEvents(t *testing.T) {
	dsn := migrationTestDSN(t)
	name := fmt.Sprintf("events_%d", time.Now().UnixNano())
	boot, err := sql.Open("pgx", swapDB(dsn, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	createClone(t, boot, name, dsnDB(dsn))
	boot.Close()
	testDSN := swapDB(dsn, name)

	b, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	sink := &pgCaptureSink{}
	ont := b.Ontology()
	setter, ok := ont.(interface{ SetEventSink(events.Sink) })
	if !ok {
		t.Fatal("postgres ontologyStore must implement SetEventSink")
	}
	setter.SetEventSink(events.BindWorkspace(sink, "ws"))

	raw, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, id := range []string{"ev-a", "ev-b"} {
		if _, err := raw.Exec(`INSERT INTO entities (id,type,name) VALUES ($1,'concept',$1)
		                       ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
	}

	err = ont.AddRelation(store.Relation{
		ID: "ev-rel-1", SourceID: "ev-a", TargetID: "ev-b", Relation: "extends",
		Confidence: 0.9, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("AddRelation: %v", err)
	}

	if got := sink.count(events.TypeEdgeAdded); got != 1 {
		t.Fatalf("edge_added events = %d, want 1", got)
	}
}
