package llm

// Regression suite for issue #114: 200-OK responses with truncated /
// unparseable bodies must be retried (transient provider flakiness), while
// well-formed-but-invalid bodies must fail fast (deterministic).
//
// These tests swap the package-level backoffDelayFn — do NOT add
// t.Parallel() to this file (precedent: compiler/watch_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/metrics"
)

// fastBackoff swaps in a near-zero backoff for the duration of a test.
func fastBackoff(t *testing.T) {
	t.Helper()
	orig := backoffDelayFn
	backoffDelayFn = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { backoffDelayFn = orig })
}

// R1 — direct path: a 200 whose body does not parse must be retried.
func TestRepro114_TruncatedBodyOn200(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}],"model":"m"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	resp, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err != nil {
		t.Fatalf("want retry then success; got error after %d attempt(s): %v", calls.Load(), err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q", resp.Content)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

// R2 — a connection dropped mid-body is a TRANSPORT failure. io.ReadAll
// returns io.ErrUnexpectedEOF; it must be propagated (not laundered into a
// parse error) and retried.
//
// Two assertions, because either alone can pass under a wrong fix:
//   - errors.Is(io.ErrUnexpectedEOF): the transport error is propagated.
//   - calls == maxAttempts: a dropped connection is transient and must retry.
func TestRepro114_ReadErrorNotLaunderedAsParseError(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "400") // promise more than we send
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
		if hj, ok := w.(http.Hijacker); ok {
			if conn, buf, err := hj.Hijack(); err == nil {
				buf.Flush() // deliver the buffered partial body before closing
				conn.Close()
			}
		}
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	_, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		// SPEC-08 structured.go fix: cancelAttempt firing BEFORE ReadAll
		// destroyed this error identity via Go's HTTP transport close
		// path — now armed AFTER the body is consumed, so the read
		// error survives deterministically.
		t.Errorf("read error laundered — want errors.Is(io.ErrUnexpectedEOF); got: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("attempts = %d, want 4 (exhausted maxAttempts)", got)
	}
}

// R3 — cached path (cache.go). A truncated cached 200 falls back to the
// direct path, which retries with validation.
//
// SCOPE NOTE: in production this path is reached by GEMINI ONLY
// (anthropic SetupCache returns "" so cacheID == "" routes direct). This
// test calls ChatCompletionCachedCtx directly to exercise the
// provider-agnostic defect.
func TestRepro114_CachedPathNoRetry(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			w.Write([]byte(`{"content":[{"type":"text","text":"hel`))
			return
		}
		w.Write([]byte(`{"content":[{"type":"text","text":"hello"}],"model":"m","stop_reason":"end_turn"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("anthropic", "k", srv.URL, 0)
	resp, err := c.ChatCompletionCachedCtx(context.Background(), "cache-id", []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err != nil {
		t.Fatalf("want retry then success; got error after %d attempt(s): %v", calls.Load(), err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q", resp.Content)
	}
}

// R4 — the guard against over-retrying. An empty-choices 200 is a
// DETERMINISTIC parse error: it must fail on the first attempt, not burn
// 4x tokens and backoff to fail identically.
func TestRepro114_DeterministicParseErrorDoesNotRetry(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[],"model":"m"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	_, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (deterministic error must not retry)", got)
	}
}

// Permanent truncation exhausts the shared retry budget and fails.
// Also pins the final-attempt guard: 4 requests → exactly 3 counted
// retries (no increment when no retry follows).
func TestRepro114_TruncationExhaustsRetries(t *testing.T) {
	fastBackoff(t)
	metrics.ResetForTest()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	_, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("attempts = %d, want 4", got)
	}
	if !strings.Contains(err.Error(), "max retries exceeded") {
		t.Errorf("error = %v, want max-retries wrap", err)
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %v, want errLabel preserved", err)
	}
	if got, ok := snapshotValue("llm_retries_total"); !ok || got.(int64) != 3 {
		t.Errorf("retries_total = %v %v, want 3 (4 attempts, final-attempt guard)", got, ok)
	}
}

// Mixed [429, truncation...]: the final error must NOT be a typed
// RateLimitError (lastStatusCode reset — decisions.md accepted trade-off),
// while the 429 still increments llm_rate_limited_total exactly once
// (first-discrimination contract).
func TestRepro114_Mixed429ThenTruncationNotTyped(t *testing.T) {
	fastBackoff(t)
	metrics.ResetForTest()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`rate limited`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	_, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsRateLimitError(err) {
		t.Errorf("mixed [429, truncation] must not surface as RateLimitError; got: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("attempts = %d, want 4", got)
	}
	if got, ok := snapshotValue("llm_rate_limited_total"); !ok || got.(int64) != 1 {
		t.Errorf("rate_limited_total = %v %v, want 1", got, ok)
	}
}

// Cached path, negative side: a DETERMINISTIC parse error on a cached 200
// (well-formed but wrong shape — json.UnmarshalTypeError, not truncation-
// class) must fail fast with NO fallback to the direct path.
func TestRepro114_CachedDeterministicParseErrorNoFallback(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content":"notanarray"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("anthropic", "k", srv.URL, 0)
	_, err := c.ChatCompletionCachedCtx(context.Background(), "cache-id", []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (deterministic cached error must not fall back)", got)
	}
}

// A parse retry that actually runs increments llm_retries_total exactly
// once (P2-2 counting contract extended to the truncation branch).
func TestRepro114_ParseRetryCountsMetric(t *testing.T) {
	fastBackoff(t)
	metrics.ResetForTest()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"model":"m"}`))
	}))
	defer srv.Close()

	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	if _, err := c.ChatCompletionCtx(context.Background(), []Message{{Role: "user", Content: "hi"}}, CallOpts{}); err != nil {
		t.Fatal(err)
	}
	if got, ok := snapshotValue("llm_retries_total"); !ok || got.(int64) != 1 {
		t.Errorf("retries_total = %v %v, want 1 (one parse retry ran)", got, ok)
	}
}

// Cancellation reaches the truncation-branch backoff: a cancel during the
// sleep returns promptly with ctx.Err, not after the full delay.
func TestRepro114_CtxCancelDuringParseBackoff(t *testing.T) {
	orig := backoffDelayFn
	backoffDelayFn = func(int) time.Duration { return 5 * time.Second }
	t.Cleanup(func() { backoffDelayFn = orig })

	var once sync.Once
	firstReq := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(firstReq) })
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"hel`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c, _ := NewClient("openai-compatible", "k", srv.URL, 0)
	errCh := make(chan error, 1)
	go func() {
		_, err := c.ChatCompletionCtx(ctx, []Message{{Role: "user", Content: "hi"}}, CallOpts{})
		errCh <- err
	}()
	<-firstReq
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel during parse-retry backoff did not return promptly")
	}
}

// syntaxCheckedStructuredStub is a native-mechanism provider whose
// ParseStructuredResponse genuinely syntax-checks the body — so a
// truncated body fails validation inside the retry loop.
type syntaxCheckedStructuredStub struct {
	url string
}

func (p syntaxCheckedStructuredStub) Name() string { return "stub" }
func (p syntaxCheckedStructuredStub) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return nil, nil
}
func (p syntaxCheckedStructuredStub) ParseResponse(body []byte) (*Response, error) {
	return &Response{Content: "plain", Model: "stub-model", Usage: Usage{InputTokens: 1, OutputTokens: 1}}, nil
}
func (p syntaxCheckedStructuredStub) SupportsVision() bool { return false }
func (p syntaxCheckedStructuredStub) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	return func() (*http.Request, error) {
		return http.NewRequest("POST", p.url, nil)
	}, true, nil
}
func (p syntaxCheckedStructuredStub) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Structured native path: a truncated 200 body retries inside the loop;
// the captured payload from the successful attempt is used (no re-parse).
func TestRepro114_StructuredTruncatedBodyRetries(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.WriteHeader(http.StatusOK)
		if n == 1 {
			w.Write([]byte(`{"id":`))
			return
		}
		w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	c := newStructuredClient(syntaxCheckedStructuredStub{url: srv.URL}, srv)
	schema := JSONSchema{Name: "t", Schema: map[string]any{"type": "object"}}
	payload, _, err := c.StructuredCompletion(context.Background(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{})
	if err != nil {
		t.Fatalf("want retry then success; got error after %d attempt(s): %v", calls.Load(), err)
	}
	if string(payload) != `{"id":1}` {
		t.Errorf("payload = %s", payload)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2", got)
	}
}

// Structured degrade path: 400 (constraint rejected) → degraded retry →
// truncated 200 → valid 200. The degraded request is validated too.
func TestRepro114_StructuredDegradeRetryAlsoValidated(t *testing.T) {
	fastBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"response_format not supported"}}`))
		case 2:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"id\":`))
		default:
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"id\":1}"}}],"model":"m"}`))
		}
	}))
	defer srv.Close()

	c, _ := NewClient("openai", "k", srv.URL, 0)
	schema := JSONSchema{Name: "t", Schema: map[string]any{"type": "object"}}
	payload, _, err := c.StructuredCompletion(context.Background(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{})
	if err != nil {
		t.Fatalf("want degrade + retry then success; got error after %d attempt(s): %v", calls.Load(), err)
	}
	if string(payload) != `{"id":1}` {
		t.Errorf("payload = %s", payload)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (400, truncated, valid)", got)
	}
}
