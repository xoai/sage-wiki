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
	// The stream's slot is gone.
	if got := bus.SinkCount(); got != 0 {
		t.Errorf("SinkCount = %d after shutdown, want 0", got)
	}
}
