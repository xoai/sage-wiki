package ontology

import (
	"time"

	"github.com/xoai/sage-wiki/pkg/events"
)

// SPEC-07 edge-event seam. The sink is installed via SetEventSink (narrow
// setter, type-asserted by the injection sites — NOT part of the
// store.OntologyStore interface) and is expected to be workspace-bound
// (events.BindWorkspace) by the installer: stores do not know their
// workspace. Emission is one implementation per behavior (ground rule 2):
// both store backends call the EmitEdge* helpers below.

// SetEventSink installs the event sink on the sqlite store (nil = no
// events).
func (s *Store) SetEventSink(sink events.Sink) {
	s.sink = events.NilSafe(sink) // typed-nil guard — see events.NilSafe
}

// emitEdgeAdded reports a relation added through this store.
func (s *Store) emitEdgeAdded(r Relation) {
	if s.sink == nil {
		return
	}
	EmitEdgeAdded(s.sink, r.ID, r.Relation, r.SourceID, r.TargetID, parseValid(r.ValidFrom))
}

// emitEdgeInvalidated reports one invalidated edge.
func (s *Store) emitEdgeInvalidated(edgeID, reason string, validTo *time.Time) {
	if s.sink == nil {
		return
	}
	EmitEdgeInvalidated(s.sink, edgeID, reason, nil, validTo)
}

// EmitEdgeAdded is the shared emission used by every store backend.
// Workspace is filled by the installer's BindWorkspace wrapper.
func EmitEdgeAdded(sink events.Sink, edgeID, relation, from, to string, validFrom *time.Time) {
	if sink == nil {
		return
	}
	sink.Emit(events.NewEvent("", events.TypeEdgeAdded, events.EdgeAdded{
		EdgeID:    edgeID,
		Relation:  relation,
		From:      from,
		To:        to,
		ValidFrom: validFrom,
	}))
}

// EmitEdgeInvalidated is the shared emission used by every store backend.
func EmitEdgeInvalidated(sink events.Sink, edgeID, reason string, validFrom, validTo *time.Time) {
	if sink == nil {
		return
	}
	sink.Emit(events.NewEvent("", events.TypeEdgeInvalidated, events.EdgeInvalidated{
		EdgeID:    edgeID,
		Reason:    reason,
		ValidFrom: validFrom,
		ValidTo:   validTo,
	}))
}

// parseValid parses an RFC3339 validity stamp; nil when empty or
// unparseable (an unknown window is reported as unknown, never faked).
func parseValid(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
