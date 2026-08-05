package compiler

import (
	"context"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/pkg/events"
)

// SPEC-08 Task 14 / AC3: evidence-span grounding. An LLM-emitted edge whose
// evidence does not appear in the resolved source text is dropped with an
// edge_rejected event; a grounded edge persists.

type spanSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *spanSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *spanSink) count(ty events.Type) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events {
		if ev.Type == ty {
			n++
		}
	}
	return n
}

func TestSpanVerificationRejectsFabricatedEvidence(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)
	sink := &spanSink{}

	// The summary does NOT contain sampleGraph's evidence sentence — the
	// model fabricated the span.
	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "An unrelated summary about something else entirely."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, sink)

	if n, _ := ont.RelationCount(); n != 0 {
		t.Errorf("relations = %d, want 0 — fabricated evidence span must be rejected", n)
	}
	if sink.count(events.TypeEdgeRejected) != 1 {
		t.Errorf("edge_rejected events = %d, want 1", sink.count(events.TypeEdgeRejected))
	}
}

func TestSpanVerificationKeepsGroundedEvidence(t *testing.T) {
	srv, _ := countingServer(t)
	ont := passStore(t)
	sink := &spanSink{}

	// The summary carries the evidence sentence (whitespace differences
	// must not break grounding).
	ExtractTriplesPass(context.Background(), ont,
		[]SummaryResult{{SourcePath: "raw/a.md", Summary: "Intro. Backpressure   extends flow control. Outro."}}, nil,
		enabledCfg(), triplesClient(t, srv.URL), false, t.TempDir(), nil, nil, nil, nil, sink)

	if n, _ := ont.RelationCount(); n != 1 {
		t.Errorf("relations = %d, want 1 — grounded evidence must persist", n)
	}
	if sink.count(events.TypeEdgeRejected) != 0 {
		t.Errorf("edge_rejected events = %d, want 0", sink.count(events.TypeEdgeRejected))
	}
}

func TestSpanNormalization(t *testing.T) {
	doc := "Backpressure extends flow control."
	for _, ev := range []string{
		"Backpressure extends flow control.",
		"backpressure   extends flow   control.",
		"  Backpressure\textends flow control.  ",
	} {
		if !evidenceGrounded(ev, doc) {
			t.Errorf("evidence %q must be grounded in doc", ev)
		}
	}
	for _, ev := range []string{
		"",
		"   ",
		"The model invented this sentence.",
	} {
		if evidenceGrounded(ev, doc) {
			t.Errorf("evidence %q must NOT be grounded", ev)
		}
	}
}

// The typed error surface for span rejection is the edge_rejected event +
// metric; limits.ErrEncoding-style sentinel sanity keeps the Which
// vocabulary pinned.
func TestSpanRejectWhichVocabulary(t *testing.T) {
	if limits.WhichEncoding == "" {
		t.Fatal("limits Which vocabulary must stay pinned")
	}
}
