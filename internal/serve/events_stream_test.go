package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
)

// TestEventsStreamSSE: the SSE surface delivers bus events as `data:`
// lines, opens with a connected comment, and unsubscribes on disconnect.
func TestEventsStreamSSE(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deps.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := events.NewBus(ctx, events.WithBufferSize(16), events.WithName("test"))
	defer bus.Close()

	srv, err := New(deps, nil, Config{
		Workspace: dir,
		ReadyFn:   func() bool { return true },
		Bus:       bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", hs.URL+"/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	// The connected comment opens the stream.
	if got := nextLine(t, lines); got != ": connected" {
		t.Fatalf("first line = %q, want ': connected'", got)
	}

	// Emit after subscribing: the event arrives as one data line.
	bus.Emit(events.NewEvent("test", events.TypeEventsDropped, events.EventsDropped{Dropped: 42}))
	var dataLine string
	for {
		l := nextLine(t, lines)
		if strings.HasPrefix(l, "data: ") {
			dataLine = strings.TrimPrefix(l, "data: ")
			break
		}
	}
	var ev events.Event
	if err := json.Unmarshal([]byte(dataLine), &ev); err != nil {
		t.Fatalf("data line not an event JSON: %v", err)
	}
	if ev.Type != events.TypeEventsDropped || ev.Workspace != "test" {
		t.Errorf("event = %+v", ev)
	}

	// The connected stream holds one delivery slot on the bus.
	if got := bus.SinkCount(); got != 1 {
		t.Fatalf("SinkCount = %d while connected, want 1", got)
	}

	// Disconnect: the handler must unsubscribe without leaking — the slot
	// count returns to baseline and the bus keeps working.
	reqCancel()
	resp.Body.Close()
	waitFor(t, func() bool { return bus.SinkCount() == 0 }, "unsubscribe")
	bus.Emit(events.NewEvent("test", events.TypeEventsDropped, events.EventsDropped{Dropped: 1}))
}

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

// TestEventsStreamDisabled: no bus = 503, never a hang.
func TestEventsStreamDisabled(t *testing.T) {
	srv := testServer(t, nil) // no Bus in Config
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func nextLine(t *testing.T, lines chan string) string {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatal("stream closed unexpectedly")
		}
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SSE line")
		return ""
	}
}

// TestShutdownEndsSSEStreams (verification pass 2 F-012): an open event
// stream must not consume the drain budget — Shutdown cancels streams at
// shutdown start, so it returns promptly instead of waiting on a connection
// that never goes idle.
func TestShutdownEndsSSEStreams(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deps.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := events.NewBus(ctx, events.WithBufferSize(16), events.WithName("test"))
	defer bus.Close()

	srv, err := New(deps, nil, Config{
		Workspace:    dir,
		ReadyFn:      func() bool { return true },
		Bus:          bus,
		DrainTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// Open an SSE stream.
	req, err := http.NewRequestWithContext(ctx, "GET", hs.URL+"/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := bus.SinkCount(); got != 1 {
		t.Fatalf("SinkCount = %d, want 1 (stream connected)", got)
	}

	// Shutdown must return within the budget despite the open stream.
	done := make(chan error, 1)
	go func() { done <- srv.Shutdown() }()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Shutdown blocked on the open SSE stream (budget 2s)")
	}
	// The stream's slot is gone — Shutdown cancels the handler async, so
	// the defers may not have run yet when Shutdown returns. Poll briefly.
	deadline := time.After(500 * time.Millisecond)
	for bus.SinkCount() != 0 {
		select {
		case <-deadline:
			t.Errorf("SinkCount = %d after shutdown, want 0", bus.SinkCount())
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestTrackForShutdown_CancelsLongLivedHandler (SPEC-02 drain fix): the
// MCP streamable-HTTP GET is a long-lived SSE session that blocks on
// r.Context().Done(). It must be cancellable by the shutdown sweep
// (cancelSSEStreams), otherwise http.Server.Shutdown waits the full drain
// budget for a connected client and then interrupts in-flight compiles.
// trackForShutdown registers each request's cancel into the same set the
// sweep drains — mirroring /events/stream.
func TestTrackForShutdown_CancelsLongLivedHandler(t *testing.T) {
	srv := testServer(t, nil) // a Server with sseCancels initialized

	blocked := make(chan struct{})
	h := srv.trackForShutdown(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // simulate the MCP SSE GET blocking until cancelled
		close(blocked)
	}))
	ts := httptest.NewServer(h)
	defer ts.Close()

	done := make(chan error, 1)
	go func() {
		resp, err := http.Get(ts.URL)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	// Confirm the handler is actually blocked (long-lived), not already done.
	select {
	case <-blocked:
		t.Fatal("handler returned before the shutdown sweep — not long-lived")
	case <-time.After(80 * time.Millisecond):
	}

	// The shutdown sweep must end the in-flight handler.
	srv.cancelSSEStreams()
	select {
	case <-done:
		// GET returned: the handler's context was cancelled. PASS.
	case <-time.After(2 * time.Second):
		t.Fatal("long-lived handler not cancelled by cancelSSEStreams — the serve drain would stall on a connected /mcp client")
	}
}

// TestCancelStackSSEStreams (SPEC-02 drain, multi-workspace): the
// multi-workspace shutdown sweep must reach every per-stack Server's SSE
// cancels so a connected /w/{name}/mcp session (which holds a stack ref)
// is ended BEFORE the HTTP drain — otherwise closeAll blocks on the
// refcount wait. Pins cancelStackSSEStreams: removing it (or moving the
// call after srv.Shutdown) leaves the session uncancelled.
func TestCancelStackSSEStreams(t *testing.T) {
	reg := &stackRegistry{stacks: map[string]*workspaceStack{}}

	// Stack A: a tracked SSE session that should be cancelled.
	srvA := &Server{sseCancels: map[int64]context.CancelFunc{}}
	ctxA, cancelA := context.WithCancel(context.Background())
	srvA.registerSSE(cancelA)
	reg.stacks["a"] = &workspaceStack{name: "a", srv: srvA}

	// Stack B: nil srv — must not panic (defensive guard).
	reg.stacks["b"] = &workspaceStack{name: "b", srv: nil}

	reg.cancelStackSSEStreams()

	if ctxA.Err() == nil {
		t.Error("cancelStackSSEStreams did not cancel stack A's SSE session — multi-mode drain would hang")
	}
}
