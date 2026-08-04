package serve

import (
	"testing"

	"github.com/xoai/sage-wiki/pkg/events"
)

// TestDepsSetEventSinkTypedNil (QA round 2): under events.enable=false the
// wiring passes a nil plane through Deps.SetEventSink — a typed-nil *Bus
// must normalize to a plain nil so compile paths degrade event-free
// instead of panicking on the first Emit.
func TestDepsSetEventSinkTypedNil(t *testing.T) {
	d := &Deps{}
	var nilBus *events.Bus
	d.SetEventSink(nilBus) // typed-nil through the interface
	if d.eventSink != nil {
		t.Fatal("eventSink must be plain nil after installing a typed-nil bus")
	}
	// The mirror/worker propagation targets must survive a nil plane.
	d.SetEventSink(nil)
	if d.eventSink != nil {
		t.Fatal("eventSink must stay nil for a nil install")
	}
}
