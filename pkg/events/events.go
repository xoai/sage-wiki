// Package events defines the engine's typed event stream (SPEC-01 seam,
// SPEC-07 union). The engine emits an Event for everything meaningful it
// does; serve mode builds webhooks, metrics, and SSE streams on top.
//
// Privacy defaults (SPEC-07): events never contain document content, raw
// query text, or filesystem paths. Event.Workspace is a workspace NAME —
// NewEvent panics on a path separator so the rule is mechanical.
package events

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Sink receives events from a Workspace. Implementations must be safe for
// concurrent use and must not block — a slow sink must not stall the
// engine.
type Sink interface {
	Emit(Event)
}

// Type identifies the event variant. The union is closed: every Type maps
// to exactly one payload struct (PayloadType).
type Type string

const (
	// TypeDocCaptured: one document registered in the workspace.
	TypeDocCaptured Type = "doc_captured"
	// TypeCompileStarted: a compile job began.
	TypeCompileStarted Type = "compile_started"
	// TypeCompileDocFinished: one document's compile outcome.
	TypeCompileDocFinished Type = "compile_doc_finished"
	// TypeCompileFinished: a compile job ended.
	TypeCompileFinished Type = "compile_finished"
	// TypeEdgeAdded: a graph relation was added.
	TypeEdgeAdded Type = "edge_added"
	// TypeEdgeInvalidated: a graph relation's validity window closed.
	TypeEdgeInvalidated Type = "edge_invalidated"
	// TypeEntityResolved: an alias merged into a canonical entity.
	TypeEntityResolved Type = "entity_resolved"
	// TypePromotionTriggered: a document changed tier (either direction).
	TypePromotionTriggered Type = "promotion_triggered"
	// TypeSearchPerformed: a search completed.
	TypeSearchPerformed Type = "search_performed"
	// TypeMirrorShipped: a mirror ship pass completed.
	TypeMirrorShipped Type = "mirror_shipped"
	// TypeMirrorSnapshot: a mirror snapshot rotation completed.
	TypeMirrorSnapshot Type = "mirror_snapshot"
	// TypeUsage: an LLM usage record (SPEC-05 wire schema).
	TypeUsage Type = "usage"
	// TypeCompileSkip: a per-doc compile-skip verdict (SPEC-04 pledge).
	TypeCompileSkip Type = "compile_skip"
	// TypeEventsDropped: coalesced overflow signal from a bounded buffer.
	TypeEventsDropped Type = "events_dropped"
	// TypeLimitExceeded: a SPEC-08 resource limit rejected an operation.
	TypeLimitExceeded Type = "limit_exceeded"
	// TypeEdgeRejected: an LLM-emitted edge failed grounding (SPEC-08 span
	// verification) and was dropped.
	TypeEdgeRejected Type = "edge_rejected"
)

// Event is the event envelope. Data holds exactly the payload struct that
// payloadTypes maps to Type.
type Event struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Workspace string    `json:"workspace"` // name only — never a path
	Type      Type      `json:"type"`
	Data      any       `json:"data"`
}

// payloadTypes is the closed Type→payload mapping. Payloads are structs by
// value: json.Marshal of a struct is field-ordered and deterministic — no
// map iteration anywhere near the wire (SPEC-04 determinism, AGENTS.md
// rule 6).
var payloadTypes = map[Type]reflect.Type{
	TypeDocCaptured:        reflect.TypeOf(DocCaptured{}),
	TypeCompileStarted:     reflect.TypeOf(CompileStarted{}),
	TypeCompileDocFinished: reflect.TypeOf(CompileDocFinished{}),
	TypeCompileFinished:    reflect.TypeOf(CompileFinished{}),
	TypeEdgeAdded:          reflect.TypeOf(EdgeAdded{}),
	TypeEdgeInvalidated:    reflect.TypeOf(EdgeInvalidated{}),
	TypeEntityResolved:     reflect.TypeOf(EntityResolved{}),
	TypePromotionTriggered: reflect.TypeOf(PromotionTriggered{}),
	TypeSearchPerformed:    reflect.TypeOf(SearchPerformed{}),
	TypeMirrorShipped:      reflect.TypeOf(MirrorShipped{}),
	TypeMirrorSnapshot:     reflect.TypeOf(MirrorSnapshot{}),
	TypeUsage:              reflect.TypeOf(Usage{}),
	TypeCompileSkip:        reflect.TypeOf(CompileSkip{}),
	TypeEventsDropped:      reflect.TypeOf(EventsDropped{}),
	TypeLimitExceeded:      reflect.TypeOf(LimitExceeded{}),
	TypeEdgeRejected:       reflect.TypeOf(EdgeRejected{}),
}

// PayloadType returns the payload struct type for t, or nil for an unknown
// Type. Sinks and schema tests use it to decode/validate Data.
func PayloadType(t Type) reflect.Type {
	return payloadTypes[t]
}

// NilSafe normalizes a typed-nil Sink (e.g. a nil *Bus passed through
// the interface) to a plain nil, so downstream `sink == nil` guards keep
// working and Emit never dereferences a nil receiver. Callers that install
// sinks conditionally MUST pass them through here.
func NilSafe(s Sink) Sink {
	if s == nil {
		return nil
	}
	v := reflect.ValueOf(s)
	if (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil() {
		return nil
	}
	return s
}

// BindWorkspace wraps a sink so every event it receives carries the given
// workspace name — the seam for emitters that do not know their workspace
// (ontology stores, resolution passes). Returns nil when inner is nil, so
// nil-sink checks stay nil-safe downstream.
func BindWorkspace(inner Sink, workspace string) Sink {
	inner = NilSafe(inner)
	if inner == nil {
		return nil
	}
	return boundSink{inner: inner, workspace: workspace}
}

type boundSink struct {
	inner     Sink
	workspace string
}

func (b boundSink) Emit(ev Event) {
	ev.Workspace = b.workspace
	b.inner.Emit(ev)
}

// NewEvent builds an Event with a fresh ID and the current UTC time.
// workspace must be a workspace NAME (no path separator); data must be the
// exact payload struct for typ. Both rules panic on violation — they are
// programming errors, and a silent wrong envelope would poison every
// downstream consumer (audit trail, webhooks, goldens).
func NewEvent(workspace string, typ Type, data any) Event {
	return NewEventAt(time.Now().UTC(), workspace, typ, data)
}

// NewEventAt is NewEvent with an explicit occurrence time (bridges that
// carry their own timestamp — e.g. the usage ledger — keep it).
func NewEventAt(t time.Time, workspace string, typ Type, data any) Event {
	if strings.ContainsAny(workspace, "/\\") {
		panic(fmt.Sprintf("events: workspace %q is a path — events carry the workspace NAME only", workspace))
	}
	want := payloadTypes[typ]
	if want == nil {
		panic(fmt.Sprintf("events: unknown event type %q", typ))
	}
	if data == nil || reflect.TypeOf(data) != want {
		panic(fmt.Sprintf("events: payload for %s must be %s, got %T", typ, want, data))
	}
	return Event{
		ID:        newID(t),
		Time:      t.UTC(),
		Workspace: workspace,
		Type:      typ,
		Data:      data,
	}
}
