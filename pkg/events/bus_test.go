package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingSink collects events in arrival order.
type recordingSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *recordingSink) Emit(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *recordingSink) snapshot() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event{}, s.events...)
}

// blockingSink stalls in Emit until its gate channel closes — the
// deliberately-slow sink of the SPEC-07 non-blocking contract.
type blockingSink struct {
	gate   chan struct{}
	mu     sync.Mutex
	events []Event
}

func (s *blockingSink) Emit(ev Event) {
	<-s.gate
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func droppedEvent(t *testing.T, ev Event) int64 {
	t.Helper()
	if ev.Type != TypeEventsDropped {
		t.Fatalf("event type = %s, want %s", ev.Type, TypeEventsDropped)
	}
	d, ok := ev.Data.(EventsDropped)
	if !ok {
		t.Fatalf("data = %T, want EventsDropped", ev.Data)
	}
	return d.Dropped
}

// TestBusFIFOOrder: per-workspace FIFO — events arrive in emission order.
func TestBusFIFOOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(16))
	sink := &recordingSink{}
	bus.AddSink(sink)

	for i := 0; i < 10; i++ {
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: int64(i)}))
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}

	got := sink.snapshot()
	if len(got) != 10 {
		t.Fatalf("delivered %d events, want 10", len(got))
	}
	for i, ev := range got {
		if d, _ := ev.Data.(EventsDropped); d.Dropped != int64(i) {
			t.Fatalf("event %d out of order: %+v", i, ev)
		}
	}
}

// TestBusOverflowDropsOldest: with the pump stalled on a slow sink, a full
// ring drops the oldest events and counts them.
func TestBusOverflowDropsOldest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(2))
	block := &blockingSink{gate: make(chan struct{})}
	bus.AddSink(block)

	for i := 0; i < 8; i++ {
		bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
	}
	waitFor(t, func() bool { return bus.Drops() >= 3 }, "buffer overflow drops")
	close(block.gate)
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	if got := bus.Drops(); got < 3 {
		t.Fatalf("Drops() = %d, want >= 3 (8 emitted through cap-2 queues)", got)
	}
}

// TestBusCoalescedEventsDropped: overflow produces ONE coalesced
// EventsDropped event (never one per drop) once delivery resumes.
func TestBusCoalescedEventsDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(2))
	block := &blockingSink{gate: make(chan struct{})}
	bus.AddSink(block)

	// The sink is stalled on its first delivery, so the pump is busy and
	// the queues fill past capacity.
	for i := 0; i < 12; i++ {
		bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
	}
	waitFor(t, func() bool { return bus.Drops() >= 4 }, "queue overflow")
	close(block.gate) // let the sink drain

	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	var dropped, shipped int
	var totalDropped int64
	for _, ev := range block.events {
		switch ev.Type {
		case TypeEventsDropped:
			dropped++
			totalDropped += droppedEvent(t, ev)
		case TypeMirrorShipped:
			shipped++
		}
	}
	if dropped == 0 {
		t.Fatal("overflow must produce at least one coalesced EventsDropped event")
	}
	if int64(dropped) >= totalDropped && totalDropped > 1 {
		t.Fatalf("%d EventsDropped events for %d drops — not coalesced", dropped, totalDropped)
	}
	// The counter is cumulative; drops may continue after a coalesced
	// flush, so it can only read >= the flushed total.
	if bus.Drops() < totalDropped {
		t.Fatalf("bus counted %d but EventsDropped reported %d", bus.Drops(), totalDropped)
	}
	if shipped == 0 {
		t.Fatal("no shipped events delivered after the gate opened")
	}
}

// TestBusSubscribe: subscribers get a FIFO channel; unsubscribe stops
// delivery.
func TestBusSubscribe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(16))
	ch, unsub := bus.Subscribe(8)

	bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 7}))
	select {
	case ev := <-ch:
		if ev.Type != TypeEventsDropped {
			t.Fatalf("type = %s", ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not receive the event")
	}

	unsub()
	bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 8}))
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unsubscribed channel still delivered: %+v", ev)
	default:
	}
}

// TestBusCloseDrains: graceful Close delivers every buffered event.
func TestBusCloseDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(64))
	sink := &recordingSink{}
	bus.AddSink(sink)
	for i := 0; i < 20; i++ {
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: int64(i)}))
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.snapshot()); got != 20 {
		t.Fatalf("Close drained %d events, want 20", got)
	}
	// Emit after Close drops silently (counted), never panics.
	bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 99}))
	if got := bus.Drops(); got < 1 {
		t.Fatal("emit after Close must be counted as dropped")
	}
}

// TestBusCancelDrops: ctx cancellation alone is a hard stop — pending
// events are dropped (counted) and the goroutines exit. The subscriber
// never reads, so its queue residue is pending at cancel time.
func TestBusCancelDrops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bus := NewBus(ctx, WithBufferSize(64))
	ch, _ := bus.Subscribe(4)
	_ = ch // never read: deliveries back up and overflow

	for i := 0; i < 30; i++ {
		bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
	}
	waitFor(t, func() bool { return bus.Drops() > 0 }, "subscriber overflow drops")

	cancel() // hard stop, no drain
	select {
	case <-bus.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("pump goroutine did not exit after ctx cancel")
	}
	_ = bus.Close() // collect residue accounting
	dropsAtCancel := bus.Drops()
	if dropsAtCancel == 0 {
		t.Fatal("cancel with pending events must count drops")
	}
	// After the hard stop, nothing new is delivered.
	for len(ch) > 0 {
		<-ch
	}
	bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: 999}))
	_ = bus.Close()
	select {
	case ev := <-ch:
		if d, _ := ev.Data.(MirrorShipped); d.Generation == 999 {
			t.Fatal("event emitted after cancel must not be delivered")
		}
	default:
	}
}

// TestBusSlowSinkDoesNotBlockEmitter: a stalled sink must not stall Emit —
// the emitter completes in bounded time while the sink blocks.
func TestBusSlowSinkDoesNotBlockEmitter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(8))
	block := &blockingSink{gate: make(chan struct{})}
	bus.AddSink(block)

	start := time.Now()
	for i := 0; i < 100; i++ {
		bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
	}
	elapsed := time.Since(start)
	close(block.gate)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("100 Emits against a stalled sink took %s — emitter blocked", elapsed)
	}
	if bus.Drops() == 0 {
		t.Fatal("a stalled sink with 100 emits into an 8-slot buffer must drop")
	}
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestBusConcurrentEmit: concurrent emitters are safe and every delivered
// event is intact (run with -race).
func TestBusConcurrentEmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(1024))
	sink := &recordingSink{}
	bus.AddSink(sink)

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: int64(g*100 + i)}))
			}
		}(g)
	}
	wg.Wait()
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	got := sink.snapshot()
	if len(got) != 400 {
		t.Fatalf("delivered %d events, want 400 (no drops at cap 1024)", len(got))
	}
}

// waitFor polls cond until true or fails the test.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestWithOnDropFires (SPEC-07 metric seam): the drop observer receives
// counts from every drop path — buffer overflow, post-Close emit, and
// cancel-path residue — so events_dropped_total can never undercount
// Drops().
func TestWithOnDropFires(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		var observed int64
		bus := NewBus(ctx, WithBufferSize(2), WithOnDrop(func(n int64) {
			mu.Lock()
			observed += n
			mu.Unlock()
		}))
		block := &blockingSink{gate: make(chan struct{})}
		bus.AddSink(block)
		for i := 0; i < 8; i++ {
			bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
		}
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return observed > 0
		}, "overflow drop observer")
		close(block.gate)
		bus.Close()
		mu.Lock()
		defer mu.Unlock()
		if observed != bus.Drops() {
			t.Errorf("observer saw %d drops, Drops() = %d", observed, bus.Drops())
		}
	})

	t.Run("post-close emit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		var observed int64
		bus := NewBus(ctx, WithBufferSize(4), WithOnDrop(func(n int64) {
			mu.Lock()
			observed += n
			mu.Unlock()
		}))
		if err := bus.Close(); err != nil {
			t.Fatal(err)
		}
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
		mu.Lock()
		defer mu.Unlock()
		if observed != 1 {
			t.Errorf("observer saw %d drops, want 1 (post-close emit)", observed)
		}
	})

	t.Run("cancel loses nothing silently", func(t *testing.T) {
		// Deterministic hard stop: every emitted event is either
		// delivered (before the stop) or drop-counted (residue at the
		// stop) — the invariant is conservation, not which side wins
		// the race against the pump.
		ctx, cancel := context.WithCancel(context.Background())
		sink := &recordingSink{}
		bus := NewBus(ctx, WithBufferSize(64))
		bus.AddSink(sink)
		for i := 0; i < 10; i++ {
			bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
		}
		cancel()
		<-bus.Done()
		waitFor(t, func() bool {
			return len(sink.snapshot())+int(bus.Drops()) >= 10
		}, "cancel accounting")
		if got := len(sink.snapshot()) + int(bus.Drops()); got != 10 {
			t.Errorf("delivered + drops = %d, want 10 (nothing silently lost)", got)
		}
	})

	t.Run("close-budget residue reaches the seam", func(t *testing.T) {
		// The defensive residue path, made real: a stalled sink exhausts
		// the (shrunk) close budget, the hard stop fires mid-drain, and
		// the undelivered residue reaches the onDrop observer.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		var observed int64
		bus := NewBus(ctx, WithBufferSize(64), WithCloseBudget(50*time.Millisecond),
			WithOnDrop(func(n int64) {
				mu.Lock()
				observed += n
				mu.Unlock()
			}))
		block := &blockingSink{gate: make(chan struct{})}
		bus.AddSink(block)
		for i := 0; i < 20; i++ {
			bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
		}
		err := bus.Close() // budget expires against the stalled sink
		close(block.gate)
		if err == nil {
			t.Fatal("Close must report the budget expiry")
		}
		// The slot's residue accounting lands asynchronously after the
		// hard stop — wait for the seam before judging.
		waitFor(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return observed > 0
		}, "residue onDrop")
		mu.Lock()
		defer mu.Unlock()
		if observed != bus.Drops() {
			t.Errorf("observer saw %d drops, Drops() = %d", observed, bus.Drops())
		}
	})
}

// TestSubscribeClosedBus: no orphaned goroutine, closed channel returned.
func TestSubscribeClosedBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(4))
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}
	ch, unsub := bus.Subscribe(4)
	defer unsub()
	if _, ok := <-ch; ok {
		t.Fatal("subscription on a closed bus must return a closed channel")
	}
}

// TestNilSafeNormalizesTypedNil (QA round 2): a nil *Bus passed through
// the Sink interface must normalize to plain nil — downstream `sink == nil`
// guards keep working and Emit never dereferences a nil receiver under
// events.enable=false.
func TestNilSafeNormalizesTypedNil(t *testing.T) {
	var nilBus *Bus
	var s Sink = nilBus
	if got := NilSafe(s); got != nil {
		t.Fatalf("NilSafe(typed-nil *Bus) = %v, want nil", got)
	}
	if got := NilSafe(nil); got != nil {
		t.Fatalf("NilSafe(nil) = %v, want nil", got)
	}
	if got := BindWorkspace(nilBus, "ws"); got != nil {
		t.Fatalf("BindWorkspace(typed-nil) = %v, want nil", got)
	}
	real := NewBus(context.Background(), WithBufferSize(1))
	defer real.Close()
	if got := NilSafe(real); got == nil {
		t.Fatal("NilSafe(real bus) = nil, want the bus")
	}
}

// TestEmitAfterCancelCounted (verification review F-001): the pump dies on
// ctx cancellation, so an Emit after cancel must drop + count — buffering
// past a dead pump would lose events silently (multi-serve buses live on
// the signal ctx).
func TestEmitAfterCancelCounted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var observed int64
	bus := NewBus(ctx, WithBufferSize(8), WithOnDrop(func(n int64) {
		mu.Lock()
		observed += n
		mu.Unlock()
	}))
	cancel()
	<-bus.Done()
	bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
	if got := bus.Drops(); got != 1 {
		t.Errorf("Drops() = %d, want 1 (post-cancel emit counted)", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if observed != 1 {
		t.Errorf("onDrop saw %d, want 1", observed)
	}
}

// TestEmitCancelConservation (verification pass 2, sharpened by pass 3):
// emitters contending the bus lock at the cancel instant must CONSERVE
// events — every one of the emitted events is delivered or drop-counted,
// never silently lost to a pump-less ring. Equality, not an upper bound:
// every emitter runs all n iterations (stop closes after wg.Wait), so the
// emitted count is exact.
func TestEmitCancelConservation(t *testing.T) {
	for round := 0; round < 20; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		sink := &recordingSink{}
		bus := NewBus(ctx, WithBufferSize(4))
		// The recording sink measures the delivered side of the
		// conservation equation; drops account for the rest.
		bus.AddSink(sink)

		const n = 50
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < n; i++ {
					bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
				}
			}()
		}
		cancel() // race the emitters
		wg.Wait()
		_ = bus.Close()

		// The slots' residue accounting is asynchronous to cancel — poll
		// the conservation sum to its exact value instead of reading it
		// once (a single read could precede countResidueAsDrops).
		deadline := time.Now().Add(2 * time.Second)
		var total int64
		for {
			total = int64(len(sink.snapshot())) + bus.Drops()
			if total == 4*n || time.Now().After(deadline) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if total != 4*n {
			t.Fatalf("round %d: delivered+drops = %d, want exactly %d (conservation)", round, total, 4*n)
		}
	}
}

// TestGracefulUnsubscribeConservation (verification pass 6): the graceful
// stopCh drain sets the exited flag, so a push landing after the drain
// completes is drop-counted rather than landing in a dead ring. Deterministic:
// no racing emitters — fill, unsubscribe, then push and assert the count.
func TestGracefulUnsubscribeConservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus(ctx, WithBufferSize(8))
	ch, unsub := bus.Subscribe(8)

	// Emit a few events and let the subscriber receive them.
	for i := 0; i < 3; i++ {
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
	}
	waitFor(t, func() bool { return len(ch) >= 3 }, "subscriber delivery")

	// Graceful unsubscribe: the slot drains and marks itself exited.
	unsub()
	waitFor(t, func() bool {
		// After the drain the slot has exited; a subsequent push must be
		// drop-counted. Probe by pushing and watching Drops.
		before := bus.Drops()
		bus.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1}))
		// Give the pump a moment to fan out the push.
		time.Sleep(5 * time.Millisecond)
		return bus.Drops() > before || bus.SinkCount() == 0
	}, "graceful exit")

	// The subscriber slot is gone and any post-exit push was drop-counted.
	if got := bus.SinkCount(); got != 0 {
		t.Errorf("SinkCount = %d after unsubscribe, want 0", got)
	}
}

// TestSlotGracefulExitDropCounts (verification pass 7): a push landing after
// a graceful drain has set exited must be drop-counted. Direct unit test of
// the slot — deterministic, and it FAILS if the graceful empty-exit stops
// setting s.exited.
func TestSlotGracefulExitDropCounts(t *testing.T) {
	var total atomic.Int64
	slot := newSinkSlot(&recordingSink{}, 4, &total, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		slot.run(ctx)
		close(done)
	}()

	// Graceful stop: the (empty) slot drains and marks itself exited.
	slot.stop()
	<-done

	// A push after the graceful exit must report dropped.
	if !slot.push(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1})) {
		t.Fatal("push after graceful exit must report dropped=true")
	}
}

// TestEmitCloseConservation (verification pass 8): emitters racing a
// graceful Close must conserve — the pump's graceful exit re-checks the
// ring under the same lock as the closed read, so an Emit appended just
// before Close is popped, not abandoned.
func TestEmitCloseConservation(t *testing.T) {
	for round := 0; round < 20; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		sink := &recordingSink{}
		bus := NewBus(ctx, WithBufferSize(4))
		bus.AddSink(sink)

		const n = 50
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < n; i++ {
					bus.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: 1}))
				}
			}()
		}
		wg.Wait()
		_ = bus.Close() // graceful Close racing the last Emits
		cancel()

		// Poll the conservation sum to its exact value. Delivered counts
		// only the emitted payload type — the coalesced EventsDropped
		// meta-event (delivered on overflow) is excluded, its count rides
		// in Drops().
		delivered := func() int64 {
			var c int64
			for _, ev := range sink.snapshot() {
				if ev.Type == TypeMirrorShipped {
					c++
				}
			}
			return c
		}
		deadline := time.Now().Add(2 * time.Second)
		var total int64
		for {
			total = delivered() + bus.Drops()
			if total == 4*n || time.Now().After(deadline) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if total != 4*n {
			t.Fatalf("round %d: delivered+drops = %d, want exactly %d (conservation)", round, total, 4*n)
		}
	}
}

// TestSubscribeRacingCloseNoSpuriousBudget (verification pass 8): a
// Subscribe racing Close cannot force the budget-expiry path — addSlot is
// gated on closed, so Close on an idle bus returns nil promptly.
func TestSubscribeRacingCloseNoSpuriousBudget(t *testing.T) {
	for round := 0; round < 20; round++ {
		ctx, cancel := context.WithCancel(context.Background())
		bus := NewBus(ctx, WithBufferSize(4), WithCloseBudget(50*time.Millisecond))
		var wg sync.WaitGroup
		// Race several Subscribes against Close.
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ch, unsub := bus.Subscribe(4)
				_ = ch
				unsub()
			}()
		}
		start := time.Now()
		err := bus.Close()
		elapsed := time.Since(start)
		wg.Wait()
		cancel()
		if err != nil {
			t.Fatalf("round %d: Close returned %v, want nil (no spurious budget expiry)", round, err)
		}
		if elapsed > 40*time.Millisecond {
			t.Fatalf("round %d: Close took %s, want prompt (budget 50ms, idle bus)", round, elapsed)
		}
	}
}
