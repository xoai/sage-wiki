package events

import (
	"context"
	"testing"
	"time"
)

// slowSink sleeps inside Emit — the deliberately-pathological sink of the
// SPEC-07 non-blocking contract.
type slowSink struct {
	delay time.Duration
}

func (s slowSink) Emit(Event) {
	time.Sleep(s.delay)
}

// TestSlowSinkDoesNotBlockEmitter (AC-2 gate): with a sink that sleeps
// 5ms per event, emitting 200 events must NOT take anywhere near
// 200×5ms — a blocking design would. The wall-clock ratio is the
// assertion (deterministic; no benchmark-comparison noise).
func TestSlowSinkDoesNotBlockEmitter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(8))
	bus.AddSink(slowSink{delay: 5 * time.Millisecond})

	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
	}
	elapsed := time.Since(start)

	// A blocking engine would take ≥ n×5ms = 1s. Assert < 100ms: two
	// orders of headroom below blocking, far above any scheduling noise.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("%d Emits against a 5ms-slow sink took %s — emitter blocked", n, elapsed)
	}
	// Overflow must have been counted (8-slot buffer vs 200 emits behind
	// a stalled sink).
	if bus.Drops() == 0 {
		t.Fatal("a stalled sink with 200 emits into an 8-slot buffer must drop")
	}
	bus.Close()
}

// BenchmarkSlowSink is the AC-2 delta evidence for the PR: compare
// against the no-sink baseline with benchcmp. The engine-side cost of a
// slow sink must stay within the SPEC-07 2% guard — the emitter never
// waits on delivery, so the delta is the ring-buffer overhead alone.
// StopTimer excludes the Close drain (a stalled sink drains at its own
// pace — that is delivery cost, not emitter cost).
func BenchmarkSlowSink(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.Run("no-sink", func(b *testing.B) {
		bus := NewBus(ctx, WithBufferSize(1024))
		ev := NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1})
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			bus.Emit(ev)
		}
		b.StopTimer()
		bus.Close()
	})

	b.Run("slow-sink", func(b *testing.B) {
		bus := NewBus(ctx, WithBufferSize(1024))
		bus.AddSink(slowSink{delay: time.Millisecond})
		ev := NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1})
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			bus.Emit(ev)
		}
		b.StopTimer()
		bus.Close()
	})
}
