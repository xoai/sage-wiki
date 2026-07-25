package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/store"
)

func TestCompileStatusEndpoint(t *testing.T) {
	srv := setupTestProject(t)

	items := srv.backend.CompileItems()
	items.Upsert(store.CompileItem{SourcePath: "a.md", Hash: "h", Tier: 1, PassIndexed: true, PassEmbedded: true})
	items.Upsert(store.CompileItem{SourcePath: "b.md", Hash: "h", Tier: 1, PassIndexed: true})
	if _, err := items.Claim(1, "worker-1", time.Hour, 10); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/compile/status", nil)
	req.Host = "127.0.0.1:3333"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Pending     int    `json:"pending"`
		Leased      int    `json:"leased"`
		Done        int    `json:"done"`
		Failed      int    `json:"failed"`
		ActiveOwner string `json:"active_owner"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pending != 1 || body.Leased != 1 {
		t.Errorf("counts: %+v, want pending=1 leased=1", body)
	}
	if body.ActiveOwner != "worker-1" {
		t.Errorf("active_owner = %q, want worker-1", body.ActiveOwner)
	}
}

func TestCompileProgressSSE(t *testing.T) {
	srv := setupTestProject(t)
	progress := compiler.NewProgress()
	srv.progress = progress

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", httpSrv.URL+"/api/compile/progress", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}

	// Emit events from the "worker" side; assert SSE frames arrive in order.
	// Wait for the handler's subscription first — emitting before the
	// handler reaches Subscribe drops the events (no replay), which flakes
	// under -race on a loaded runner.
	subDeadline := time.Now().Add(3 * time.Second)
	for progress.SubscriberCount() == 0 && time.Now().Before(subDeadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if progress.SubscriberCount() == 0 {
		t.Fatal("handler never subscribed")
	}
	progress.StartPhase("Tier 1: Index + embed sources", 2)
	progress.ItemDone("raw/a.md", "a.sum.md")

	scanner := bufio.NewScanner(resp.Body)
	var frames []string
	deadline := time.After(3 * time.Second)
	done := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				frames = append(frames, strings.TrimPrefix(line, "event: "))
			}
			if len(frames) >= 2 {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-deadline:
		t.Fatalf("timed out waiting for SSE frames, got %v", frames)
	}
	if frames[0] != "phase" || frames[1] != "item" {
		t.Errorf("frame order = %v, want [phase item]", frames)
	}

	// Client disconnect: the stream terminates (request ctx cancellation
	// propagates to the handler, which unsubscribes and returns — R3).
	cancel()
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 64)
		_, err := resp.Body.Read(buf)
		readDone <- err
	}()
	select {
	case <-readDone:
		// Stream ended after cancel — the handler is not still writing.
	case <-time.After(3 * time.Second):
		t.Error("stream did not terminate after client cancel — handler leak")
	}
	// Server side: the handler's unsubscribe ran (R3 — no subscriber leak).
	unsubDeadline := time.Now().Add(2 * time.Second)
	for progress.SubscriberCount() > 0 && time.Now().Before(unsubDeadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := progress.SubscriberCount(); n != 0 {
		t.Errorf("subscriber count = %d after disconnect, want 0", n)
	}
}

func TestCompileProgressSSE_NoWorker(t *testing.T) {
	srv := setupTestProject(t) // progress nil

	req := httptest.NewRequest("GET", "/api/compile/progress", nil)
	req.Host = "127.0.0.1:3333"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when no worker progress hub", rec.Code)
	}
}
