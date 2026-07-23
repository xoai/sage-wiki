package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blockingProvider builds requests to a given URL and (optionally) implements
// CachingProvider so both the direct and cached dispatch paths can be exercised.
type blockingProvider struct{ url string }

func (b blockingProvider) FormatStructuredRequest(_ []Message, _ JSONSchema, _ CallOpts) (func() (*http.Request, error), bool, error) {
	return nil, false, nil
}
func (b blockingProvider) ParseStructuredResponse([]byte) (json.RawMessage, error) {
	return nil, ErrStructuredUnsupported
}

func (b blockingProvider) Name() string         { return "blocking" }
func (b blockingProvider) SupportsVision() bool { return false }
func (b blockingProvider) FormatRequest(_ []Message, _ CallOpts) (*http.Request, error) {
	return http.NewRequest("POST", b.url, strings.NewReader("{}"))
}
func (b blockingProvider) ParseResponse(_ []byte) (*Response, error) {
	return &Response{Content: "ok"}, nil
}
func (b blockingProvider) SetupCache(_, _ string) (string, error) { return "cache-x", nil }
func (b blockingProvider) FormatCachedRequest(_ string, _ []Message, _ CallOpts) (*http.Request, error) {
	return http.NewRequest("POST", b.url, strings.NewReader("{}"))
}
func (b blockingProvider) TeardownCache(_ string) error { return nil }

func newTestClient(url string) *Client {
	return &Client{provider: blockingProvider{url}, limiter: newRateLimiter(1000)}
}

// cancel while the server is blocked → ChatCompletionCtx returns promptly with a
// ctx error, not after the full server block.
func TestChatCompletionCtx_CancelsDirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.ChatCompletionCtx(ctx, []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a context error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("did not return promptly: %v (blocked server not cancelled)", elapsed)
	}
}

// cancel mid-backoff (server 429s) → returns within one backoff interval, not
// after the full exponential sleep. Time-bounded, not just err != nil.
func TestChatCompletionCtx_CancelsDuringBackoff(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.ChatCompletionCtx(ctx, []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// backoffDelay(0) is ~1-2s; a ctx-aware backoff must return well under that.
	if elapsed > 800*time.Millisecond {
		t.Errorf("backoff not ctx-aware: returned after %v", elapsed)
	}
}

// The DEFAULT anthropic/gemini path uses caching. A direct-only test would
// false-pass, so exercise the cached dispatch with a cacheID set.
func TestChatCompletionCtx_CancelsCachedPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	c.cacheID = "cache-x" // route through ChatCompletionCached

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.ChatCompletionCtx(ctx, []Message{{Role: "user", Content: "hi"}}, CallOpts{})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled on cached path, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("cached path did not cancel promptly: %v", elapsed)
	}
}

// The old no-ctx method must still work (binary/source compat) via delegation.
func TestChatCompletion_DelegatesToCtx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer server.Close()

	c := newTestClient(server.URL)
	resp, err := c.ChatCompletion([]Message{{Role: "user", Content: "hi"}}, CallOpts{})
	if err != nil {
		t.Fatalf("ChatCompletion (delegating): %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}
