package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/pkg/events"
)

func testEvent() events.Event {
	return events.NewEvent("ws", events.TypeCompileFinished, events.CompileFinished{
		JobID: "j1", Outcome: "completed",
	})
}

// TestWebhookSignatureVerifies: the delivery carries an HMAC-SHA256
// signature that recomputes with the documented recipe.
func TestWebhookSignatureVerifies(t *testing.T) {
	var mu sync.Mutex
	var gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotSig = r.Header.Get(SignatureHeader)
		gotBody = body
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	secretFile := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secretFile, []byte("s3cret\n"), 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsDir := t.TempDir()
	d, err := NewWebhookDispatcher(ctx, wsDir, []config.WebhookConfig{{
		URL: srv.URL, SecretFile: secretFile,
	}})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(testEvent())
	waitForWebhook(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gotSig != ""
	})
	d.Stop()

	mu.Lock()
	sig, body := gotSig, gotBody
	mu.Unlock()
	if !strings.HasPrefix(sig, "sha256=") {
		t.Fatalf("signature header = %q, want sha256= prefix", sig)
	}
	if !VerifySignature([]byte("s3cret"), body, sig) {
		t.Fatal("signature does not recompute with the shared secret")
	}
	if VerifySignature([]byte("wrong"), body, sig) {
		t.Fatal("signature must not verify with the wrong secret")
	}
}

// TestWebhookTypeFilter: an endpoint with types=[compile_finished] receives
// compile_finished but NOT other types.
func TestWebhookTypeFilter(t *testing.T) {
	var mu sync.Mutex
	var types []events.Type
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ev struct {
			Type events.Type `json:"type"`
		}
		json.Unmarshal(body, &ev)
		mu.Lock()
		types = append(types, ev.Type)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := NewWebhookDispatcher(ctx, t.TempDir(), []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"),
		Types: []string{"compile_finished"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(testEvent())                                                                          // compile_finished — delivered
	d.Emit(events.NewEvent("ws", events.TypeDocCaptured, events.DocCaptured{DocID: "raw/a.md"})) // filtered out
	waitForWebhook(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(types) >= 1
	})
	// Give the (non-)delivery of the second event a moment, then assert.
	time.Sleep(100 * time.Millisecond)
	d.Stop()

	mu.Lock()
	defer mu.Unlock()
	if len(types) != 1 || types[0] != events.TypeCompileFinished {
		t.Fatalf("delivered types = %v, want exactly [compile_finished]", types)
	}
}

// TestWebhookRetryThenDeadLetter: a 500 endpoint is retried with backoff,
// then dead-lettered with the attempt count and last error.
func TestWebhookRetryThenDeadLetter(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsDir := t.TempDir()
	retries := 2
	d, err := NewWebhookDispatcher(ctx, wsDir, []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"), MaxRetries: &retries,
	}})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(testEvent())
	waitForWebhook(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"))
		return err == nil && len(raw) > 0
	})
	d.Stop()

	if got := hits.Load(); got != 3 { // 1 attempt + 2 retries
		t.Errorf("endpoint hits = %d, want 3 (1 + max_retries)", got)
	}
	raw, err := os.ReadFile(filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		URL      string `json:"url"`
		Attempts int    `json:"attempts"`
		LastErr  string `json:"last_error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &rec); err != nil {
		t.Fatalf("dead-letter line not JSON: %v", err)
	}
	if rec.Attempts != 3 || rec.LastErr == "" || rec.URL != srv.URL {
		t.Errorf("dead-letter record = %+v", rec)
	}
}

// TestWebhook4xxNoRetry: a 400 is a permanent failure — straight to the
// dead letter without retries.
func TestWebhook4xxNoRetry(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(400)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsDir := t.TempDir()
	d, err := NewWebhookDispatcher(ctx, wsDir, []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	d.Emit(testEvent())
	waitForWebhook(t, func() bool {
		_, err := os.Stat(filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"))
		return err == nil
	})
	d.Stop()
	if got := hits.Load(); got != 1 {
		t.Errorf("endpoint hits = %d, want 1 (4xx must not retry)", got)
	}
}

// TestWebhookSecretResolution: fail-fast at construction for missing
// secrets — never at delivery time.
func TestWebhookSecretResolution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := NewWebhookDispatcher(ctx, t.TempDir(), []config.WebhookConfig{{
		URL: "https://example.com", SecretEnv: "SURELY_UNSET_VAR_XYZ",
	}}); err == nil {
		t.Fatal("unset secret_env must fail construction")
	}
	if _, err := NewWebhookDispatcher(ctx, t.TempDir(), []config.WebhookConfig{{
		URL: "https://example.com", SecretFile: filepath.Join(t.TempDir(), "missing"),
	}}); err == nil {
		t.Fatal("missing secret_file must fail construction")
	}
}

func setWebhookEnv(t *testing.T, value string) string {
	t.Helper()
	name := "SAGE_WEBHOOK_TEST_SECRET"
	t.Setenv(name, value)
	return name
}

func waitForWebhook(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for webhook condition")
}

// TestWebhookHungEndpointDoesNotStall: an endpoint that never responds is
// bounded by the per-delivery timeout — the dispatcher dead-letters it and
// Emit never blocks the engine (AC-3 no-stall).
func TestWebhookHungEndpointDoesNotStall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test ends
	}))
	// Order matters: release the hung handlers BEFORE Close waits for them.
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsDir := t.TempDir()
	zero := 0
	d, err := NewWebhookDispatcher(ctx, wsDir, []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"),
		TimeoutSeconds: 1, MaxRetries: &zero,
	}})
	if err != nil {
		t.Fatal(err)
	}

	// 100 Emits against a hung endpoint must return immediately — the
	// engine never waits on delivery.
	emitStart := time.Now()
	for i := 0; i < 100; i++ {
		d.Emit(testEvent())
	}
	if elapsed := time.Since(emitStart); elapsed > 500*time.Millisecond {
		t.Fatalf("100 Emits took %s — dispatcher blocked the emitter", elapsed)
	}

	// The hung delivery dead-letters within the timeout budget.
	waitForWebhook(t, func() bool {
		_, err := os.Stat(filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"))
		return err == nil
	})
	d.Stop()
}

// TestWebhookQueueResidueDeadLettered (no silent failures): events still
// queued when the server stops get a dead-letter record, not a silent loss.
func TestWebhookQueueResidueDeadLettered(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang: the worker never returns to the queue
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsDir := t.TempDir()
	d, err := NewWebhookDispatcher(ctx, wsDir, []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"),
		TimeoutSeconds: 60,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// The worker is stuck on the first delivery; the rest queue up.
	for i := 0; i < 5; i++ {
		d.Emit(testEvent())
	}
	waitForWebhook(t, func() bool { return len(d.queue) >= 3 })
	d.Stop() // shutdown must dead-letter the queued residue

	raw, err := os.ReadFile(filepath.Join(wsDir, ".sage", "webhooks-deadletter.jsonl"))
	if err != nil {
		t.Fatalf("no dead-letter file after shutdown: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 3 {
		t.Fatalf("dead-letter lines = %d, want >= 3 (queued residue recorded)", len(lines))
	}
	if !strings.Contains(string(raw), "server stopping") {
		t.Error("residue records must carry the shutdown reason")
	}
}

// TestWebhookQueueDropsCounted: a saturated dispatcher queue (hung
// endpoint, burst of events) joins events_dropped_total — never invisible.
func TestWebhookQueueDropsCounted(t *testing.T) {
	metrics.ResetForTest()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := NewWebhookDispatcher(ctx, t.TempDir(), []config.WebhookConfig{{
		URL: srv.URL, SecretEnv: setWebhookEnv(t, "s3cret"), TimeoutSeconds: 60,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < webhookQueueSize+64; i++ {
		d.Emit(testEvent())
	}
	d.Stop()

	got := int64(-1)
	snap := metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == "events_dropped_total" {
			got = snap[i+1].(int64)
		}
	}
	if got < 32 {
		t.Fatalf("events_dropped_total = %d, want >= 32 queue drops", got)
	}
}
