package events

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultBufferSize = 1024
	closeBudget       = 2 * time.Second
	// coalesceWindow bounds EventsDropped emission to at most one event
	// per window per bus — drops accumulate in the pending counter and
	// flush together, so a stalled sink produces a bounded, coalesced
	// signal instead of one event per drop (feedback-loop guard).
	coalesceWindow = 100 * time.Millisecond
)

// Bus is a non-blocking event multiplexer: the engine calls Emit (never
// blocked, whatever the sinks do), a single pump preserves per-workspace
// FIFO order, and each registered sink gets its own bounded delivery queue
// (drop-oldest on overflow). SPEC-07 delivery contract: a slow sink must
// never stall the engine; overflow drops oldest and is counted.
type Bus struct {
	name        string
	bufSize     int
	closeBudget time.Duration
	logger      *slog.Logger

	mu           sync.Mutex
	ring         []Event
	head, size   int
	pendingDrops int64 // coalesced drop count awaiting one EventsDropped event
	closed       bool
	notify       chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the pump goroutine exits
	sinks  sync.WaitGroup

	slotsMu sync.Mutex
	slots   []*sinkSlot

	totalDrops atomic.Int64 // buffer drops + per-sink delivery drops
	onDrop     func(int64)  // optional drop observer (SPEC-07 metrics seam)
}

// BusOption configures a Bus.
type BusOption func(*Bus)

// WithBufferSize sets the ring capacity (events). Values < 1 use the
// default (1024).
func WithBufferSize(n int) BusOption {
	return func(b *Bus) {
		if n >= 1 {
			b.bufSize = n
		}
	}
}

// WithLogger sets the logger for shutdown diagnostics (drops are observed
// through counters and EventsDropped, not logs).
func WithLogger(l *slog.Logger) BusOption {
	return func(b *Bus) { b.logger = l }
}

// WithCloseBudget sets Close's drain budget (default 2s). Tests shrink it
// to exercise the budget-expiry path without waiting it out.
func WithCloseBudget(d time.Duration) BusOption {
	return func(b *Bus) {
		if d > 0 {
			b.closeBudget = d
		}
	}
}

// WithName labels the bus's own events (EventsDropped) with a workspace
// name. Empty is valid: a bus that serves no single workspace.
func WithName(name string) BusOption {
	return func(b *Bus) { b.name = name }
}

// WithOnDrop installs a drop observer, called with the number of events
// dropped at each overflow site (buffer, per-sink queue, post-Close emit).
// SPEC-07 metrics wiring point: serve installs a counter increment here
// (events_dropped_total) without coupling pkg/events to the registry. The
// callback must not block.
func WithOnDrop(fn func(n int64)) BusOption {
	return func(b *Bus) { b.onDrop = fn }
}

// NewBus starts the pump goroutine, derived from ctx (AGENTS.md rule 7).
// Shutdown semantics: Close is the graceful path (drain within the close
// budget). ctx cancellation is a deterministic HARD STOP: whatever is
// still in the buffer or a sink queue at that moment is drop-counted and
// reaches the onDrop seam — never delivered after the stop, never
// silently lost. One forfeiture on the hard path: a coalesced
// EventsDropped audit record still pending when cancellation lands is
// itself dropped — the onDrop/events_dropped_total seam stays accurate,
// only the audit-trail event is lost. Emits after Close/cancel are
// dropped and counted.
func NewBus(ctx context.Context, opts ...BusOption) *Bus {
	b := &Bus{
		bufSize:     defaultBufferSize,
		closeBudget: closeBudget,
		notify:      make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	b.ring = make([]Event, b.bufSize)
	b.ctx, b.cancel = context.WithCancel(ctx)
	go b.run()
	return b
}

// Emit enqueues an event without ever blocking. Overflow drops the oldest
// event (counted); Emit after Close OR after ctx cancellation drops the
// new event (counted) — the pump dies on cancellation, so buffering past
// it would lose events silently (multi-serve buses live on the signal
// ctx: SIGTERM must not swallow the drain's final events).
func (b *Bus) Emit(ev Event) {
	if b.ctx.Err() != nil {
		b.totalDrops.Add(1)
		if b.onDrop != nil {
			b.onDrop(1)
		}
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.totalDrops.Add(1)
		if b.onDrop != nil {
			b.onDrop(1)
		}
		return
	}
	if b.size == len(b.ring) {
		// Drop oldest; coalesce the count into one EventsDropped event.
		b.ring[b.head] = Event{}
		b.head = (b.head + 1) % len(b.ring)
		b.size--
		b.pendingDrops++
		b.totalDrops.Add(1)
		if b.onDrop != nil {
			b.onDrop(1)
		}
	}
	b.ring[(b.head+b.size)%len(b.ring)] = ev
	b.size++
	b.mu.Unlock()
	b.signal()
}

// recordDrop counts a drop at any level and folds it into the coalesced
// EventsDropped signal (SPEC-07: overflow is observable, one level deep —
// buffer, per-sink queue, and subscriber channel all feed the same count).
func (b *Bus) recordDrop() {
	b.totalDrops.Add(1)
	b.mu.Lock()
	b.pendingDrops++
	b.mu.Unlock()
	if b.onDrop != nil {
		b.onDrop(1)
	}
}

// AddSink registers a sink with its own bounded delivery queue (same
// capacity as the bus buffer). Delivery order per sink is FIFO.
func (b *Bus) AddSink(s Sink) error {
	if s == nil {
		return fmt.Errorf("events: AddSink(nil)")
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return fmt.Errorf("events: AddSink on closed bus")
	}
	b.addSlot(newSinkSlot(s, b.bufSize, &b.totalDrops, b.onDrop))
	return nil
}

// Subscribe returns a channel of events and an unsubscribe function. The
// channel is buffered; a slow subscriber gets drop-oldest semantics like
// any other sink. Subscribing a closed bus returns a closed channel and a
// no-op unsubscribe — never an orphaned delivery goroutine.
//
// CONTRACT: after subscription the channel is NEVER closed — not by
// Close, not by cancellation, not by unsubscribe. Consumers must select
// on their own context (the SSE handler does) rather than range over the
// channel, which would block forever after shutdown.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Event, buffer)
	slot := newSinkSlot(channelSink{ch: ch, bus: b}, buffer, &b.totalDrops, b.onDrop)
	if !b.addSlot(slot) {
		// Bus already closed — hand back a closed channel, never an
		// orphaned delivery goroutine.
		close(ch)
		return ch, func() {}
	}
	return ch, func() { b.removeSlot(slot) }
}

// Drops returns the total number of events dropped (buffer overflow +
// per-sink delivery overflow + post-Close emits).
func (b *Bus) Drops() int64 {
	return b.totalDrops.Load()
}

// SinkCount returns the number of attached delivery slots (sinks and
// subscribers). Introspection for tests and operator diagnostics.
func (b *Bus) SinkCount() int {
	b.slotsMu.Lock()
	defer b.slotsMu.Unlock()
	return len(b.slots)
}

// Done closes when the pump goroutine has exited.
func (b *Bus) Done() <-chan struct{} {
	return b.done
}

// Close stops accepting events, drains the buffer and every sink queue
// within the close budget, and closes sinks that implement io.Closer. If
// the budget expires the bus hard-stops (ctx cancel), the undelivered
// residue is drop-counted (and reaches the onDrop seam), and Close returns
// an error — the drop counters say what was lost.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.signal()

	deadline := time.Now().Add(b.closeBudget)
	select {
	case <-b.done:
	case <-time.After(b.closeBudget):
		b.cancel()
		<-b.done
	}
	// The pump is down. Give the delivery goroutines the REMAINING budget
	// to drain their queues; cancel only after they finish (or the budget
	// is spent) — cancelling earlier would fire the hard stop mid-drain
	// and drop what the drain was carrying.
	remaining := time.Until(deadline)
	if remaining < 0 {
		remaining = 0
	}
	if waitDone(&b.sinks, remaining) {
		// Every slot drained. Cancel collects any goroutine parked in a
		// select (embedder-leak guard — slots must not outlive the bus).
		b.cancel()
		return nil
	}
	// Budget spent: hard stop — each slot counts its residue as drops on
	// the way out (reaches the onDrop seam).
	b.cancel()
	waitDone(&b.sinks, time.Second) // brief settle for residue accounting
	if b.logger != nil {
		b.logger.Warn("events: bus close budget expired — remaining events dropped",
			"drops", b.Drops())
	}
	return fmt.Errorf("events: bus close budget (%s) expired; drops=%d", b.closeBudget, b.Drops())
}

// ── internals ──────────────────────────────────────────────────────────

func (b *Bus) signal() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *Bus) pop() (Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size == 0 {
		return Event{}, false
	}
	ev := b.ring[b.head]
	b.ring[b.head] = Event{}
	b.head = (b.head + 1) % len(b.ring)
	b.size--
	return ev, true
}

// takePendingDrops atomically collects the coalesced drop counter.
func (b *Bus) takePendingDrops() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	d := b.pendingDrops
	b.pendingDrops = 0
	return d
}

func (b *Bus) run() {
	defer close(b.done)
	var flushAt <-chan time.Time // pending coalesced flush, nil = none
	for {
		// Hard stop is deterministic: whatever is still buffered when
		// cancellation is observed is drop-counted (reaches the onDrop
		// seam) — never delivered, never silently lost. Mark closed
		// under the lock so an Emit straddling this instant drop-counts
		// instead of appending to a ring whose pump is gone.
		select {
		case <-b.ctx.Done():
			b.mu.Lock()
			b.closed = true
			b.mu.Unlock()
			b.countBufferedAsDrops()
			b.stopSinks()
			return
		default:
		}
		if ev, ok := b.pop(); ok {
			b.fanOut(ev)
			continue
		}
		b.mu.Lock()
		if b.closed {
			// Graceful drain boundary. Re-check size UNDER THE SAME LOCK
			// as the closed read: an Emit that appended just before Close
			// set closed is still in the ring — loop back and pop it
			// rather than exiting with the ring non-empty (the pump-level
			// mirror of the slot's empty-check-and-exit).
			if b.size > 0 {
				b.mu.Unlock()
				continue
			}
			pending := b.pendingDrops > 0
			b.mu.Unlock()
			// Flush the overflow signal now. NOTE: drops recorded BY this
			// flush's own fanOut (a slot ring full at the close instant)
			// or by post-pump slot drains reach only the counter/onDrop
			// seam — not a further audit event (flushing again could loop
			// while a ring stays full). This matches the documented
			// hard-path forfeiture: the counter is always accurate; the
			// JSONL audit record for the final batch may be short.
			if pending {
				if d := b.takePendingDrops(); d > 0 {
					b.fanOut(NewEvent(b.name, TypeEventsDropped, EventsDropped{Dropped: d}))
				}
			}
			b.stopSinks()
			return
		}
		pending := b.pendingDrops > 0
		b.mu.Unlock()
		if pending && flushAt == nil {
			flushAt = time.After(coalesceWindow)
		}
		select {
		case <-b.ctx.Done():
			// Mark closed under the lock so an Emit straddling this
			// instant sees it and drop-counts instead of appending to a
			// ring whose pump is gone (conservation under cancellation).
			b.mu.Lock()
			b.closed = true
			b.mu.Unlock()
			b.countBufferedAsDrops()
			b.stopSinks()
			return
		case <-b.notify:
		case <-flushAt:
			// One coalesced event per window: drops that accumulated
			// during the window (including any dropped flush events)
			// ride together.
			if d := b.takePendingDrops(); d > 0 {
				b.fanOut(NewEvent(b.name, TypeEventsDropped, EventsDropped{Dropped: d}))
			}
			flushAt = nil
		}
	}
}

func (b *Bus) countBufferedAsDrops() {
	b.mu.Lock()
	n := int64(b.size)
	for i := range b.ring {
		b.ring[i] = Event{}
	}
	b.head, b.size = 0, 0
	b.mu.Unlock()
	if n > 0 {
		b.totalDrops.Add(n)
		if b.onDrop != nil {
			b.onDrop(n)
		}
	}
}

func (b *Bus) fanOut(ev Event) {
	b.slotsMu.Lock()
	slots := make([]*sinkSlot, len(b.slots))
	copy(slots, b.slots)
	b.slotsMu.Unlock()
	for _, s := range slots {
		if s.push(ev) {
			b.recordDrop()
		}
	}
}

// addSlot registers a delivery slot, atomically gated on closed: a Subscribe
// racing Close cannot land a slot after the pump's stopSinks snapshot (the
// slot would never be stopped and would force Close down the budget-expiry
// path). Holding b.mu across the closed-check + append prevents Close from
// setting closed in between. Returns false when the bus is closed.
func (b *Bus) addSlot(s *sinkSlot) bool {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return false
	}
	b.slotsMu.Lock()
	b.slots = append(b.slots, s)
	b.slotsMu.Unlock()
	b.mu.Unlock()
	b.sinks.Add(1)
	go func() {
		defer b.sinks.Done()
		s.run(b.ctx)
	}()
	return true
}

func (b *Bus) removeSlot(s *sinkSlot) {
	b.slotsMu.Lock()
	for i, x := range b.slots {
		if x == s {
			b.slots = append(b.slots[:i], b.slots[i+1:]...)
			break
		}
	}
	b.slotsMu.Unlock()
	s.stop()
}

// stopSinks signals every delivery goroutine to drain and exit, then
// waits for them (bounded by the caller's Close budget via waitDone).
func (b *Bus) stopSinks() {
	b.slotsMu.Lock()
	slots := make([]*sinkSlot, len(b.slots))
	copy(slots, b.slots)
	b.slotsMu.Unlock()
	for _, s := range slots {
		s.stop()
	}
}

// waitDone waits for the sink WaitGroup with a timeout.
func waitDone(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ── per-sink delivery queue ────────────────────────────────────────────

type sinkSlot struct {
	sink   Sink
	drops  *atomic.Int64 // bus total — hard-stop residue is counted as drops
	onDrop func(int64)   // bus drop observer (nil-safe); residue must reach the metric seam
	mu     sync.Mutex
	ring   []Event
	head   int
	size   int
	exited bool // set under mu on the ctx-done exit; push drop-counts after it
	notify chan struct{}
	stopCh chan struct{}
	once   sync.Once
}

func newSinkSlot(s Sink, buffer int, drops *atomic.Int64, onDrop func(int64)) *sinkSlot {
	return &sinkSlot{
		sink:   s,
		drops:  drops,
		onDrop: onDrop,
		ring:   make([]Event, buffer),
		notify: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}
}

// push enqueues for delivery; drop-oldest on overflow. Returns true when
// an event was dropped.
func (s *sinkSlot) push(ev Event) bool {
	s.mu.Lock()
	// The slot's ctx-done exit counts residue under s.mu and sets exited;
	// a push landing after that would append to a dead ring — neither
	// delivered nor counted. Drop-count it instead (conservation for
	// events in the pump's hand at the cancel instant).
	if s.exited {
		s.mu.Unlock()
		return true
	}
	dropped := false
	if s.size == len(s.ring) {
		s.ring[s.head] = Event{}
		s.head = (s.head + 1) % len(s.ring)
		s.size--
		dropped = true
	}
	s.ring[(s.head+s.size)%len(s.ring)] = ev
	s.size++
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return dropped
}

func (s *sinkSlot) pop() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.size == 0 {
		return Event{}, false
	}
	ev := s.ring[s.head]
	s.ring[s.head] = Event{}
	s.head = (s.head + 1) % len(s.ring)
	s.size--
	return ev, true
}

func (s *sinkSlot) stop() {
	s.once.Do(func() { close(s.stopCh) })
}

func (s *sinkSlot) run(ctx context.Context) {
	defer func() {
		if c, ok := s.sink.(io.Closer); ok {
			c.Close()
		}
	}()
	for {
		// Hard stop mirrors the pump: queue residue at cancellation is
		// drop-counted, not delivered after the stop.
		select {
		case <-ctx.Done():
			s.exitHard()
			return
		default:
		}
		if ev, ok := s.pop(); ok {
			s.sink.Emit(ev)
			continue
		}
		select {
		case <-ctx.Done():
			s.exitHard()
			return
		case <-s.notify:
		case <-s.stopCh:
			// Graceful stop: deliver what is queued, checking ctx so a
			// hard stop still wins mid-drain.
			for {
				select {
				case <-ctx.Done():
					s.exitHard()
					return
				default:
				}
				ev, ok := s.pop()
				if !ok {
					// Ring empty: mark exited UNDER the lock, re-checking
					// size so a push landing in the same critical section
					// is either drained (size>0 → continue) or drop-counted
					// (exited set before it appends). Never return with the
					// ring non-empty and exited unset — a post-exit push
					// would land in a dead ring uncounted.
					s.mu.Lock()
					if s.size == 0 {
						s.exited = true
						s.mu.Unlock()
						return
					}
					s.mu.Unlock()
					continue
				}
				s.sink.Emit(ev)
			}
		}
	}
}

// exitHard marks the slot exited under s.mu (push drop-counts anything
// landing after it) and counts the queued residue — one convention on
// EVERY ctx-done exit path, so an in-flight push from the pump can never
// land in a dead ring uncounted.
func (s *sinkSlot) exitHard() {
	s.mu.Lock()
	s.exited = true
	s.mu.Unlock()
	s.countResidueAsDrops()
}

// countResidueAsDrops counts queued-but-undelivered events on a hard stop
// — they are drops exactly like buffer overflow.
func (s *sinkSlot) countResidueAsDrops() {
	s.mu.Lock()
	n := int64(s.size)
	for i := range s.ring {
		s.ring[i] = Event{}
	}
	s.head, s.size = 0, 0
	s.mu.Unlock()
	if n > 0 {
		if s.drops != nil {
			s.drops.Add(n)
		}
		if s.onDrop != nil {
			s.onDrop(n)
		}
	}
}

// channelSink adapts a subscriber channel to Sink. Send is non-blocking:
// the slot's ring already buffers, and a dead subscriber must not wedge
// the delivery goroutine. A full channel is a counted drop.
type channelSink struct {
	ch  chan<- Event
	bus *Bus
}

func (c channelSink) Emit(ev Event) {
	select {
	case c.ch <- ev:
	default:
		if c.bus != nil {
			c.bus.recordDrop()
		}
	}
}
