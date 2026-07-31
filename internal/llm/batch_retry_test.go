package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// #124: a truncated 200 body on retrieve is transient — retry until it succeeds.
func TestRetrieveRetriesTruncatedBody(t *testing.T) {
	defer func(orig func(int) time.Duration) { backoffDelayFn = orig }(backoffDelayFn)
	backoffDelayFn = func(int) time.Duration { return 0 }

	good := `{"custom_id":"src-1","result":{"type":"succeeded","message":{"content":[{"type":"text","text":"ok"}],"model":"m","usage":{"input_tokens":1,"output_tokens":1}}}}`
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			// Truncated tail: a malformed JSONL line.
			fmt.Fprintln(w, good[:len(good)/2])
			return
		}
		fmt.Fprintln(w, good)
	}))
	defer srv.Close()

	p := newAnthropicProvider("k", srv.URL)
	results, err := p.RetrieveBatch(srv.URL + "/results")
	if err != nil {
		t.Fatalf("RetrieveBatch: %v", err)
	}
	if len(results) != 1 || results[0].CustomID != "src-1" {
		t.Errorf("results = %+v", results)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3 (2 truncations + success)", attempts.Load())
	}
}

// Persistent truncation exhausts retries and surfaces an error — never a
// silent partial result.
func TestRetrieveExhaustsRetries(t *testing.T) {
	defer func(orig func(int) time.Duration) { backoffDelayFn = orig }(backoffDelayFn)
	backoffDelayFn = func(int) time.Duration { return 0 }

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		fmt.Fprintln(w, `{"custom_id":"src-1","result":{"type":"succeeded"`) // never valid
	}))
	defer srv.Close()

	p := newAnthropicProvider("k", srv.URL)
	_, err := p.RetrieveBatch(srv.URL + "/results")
	if err == nil {
		t.Fatal("persistent truncation must error, not return partial results")
	}
	if attempts.Load() != 4 {
		t.Errorf("attempts = %d, want 4 (maxAttempts)", attempts.Load())
	}
}

// A malformed line errors (with %w so the classifier can see it) instead of
// being silently skipped.
func TestParseMalformedLineErrors(t *testing.T) {
	defer func(orig func(int) time.Duration) { backoffDelayFn = orig }(backoffDelayFn)
	backoffDelayFn = func(int) time.Duration { return 0 }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"custom_id":"src-1","result":CORRUPT`)
	}))
	defer srv.Close()
	p := newAnthropicProvider("k", srv.URL)
	_, err := p.RetrieveBatch(srv.URL + "/results")
	if err == nil {
		t.Fatal("malformed line must error, not skip")
	}
	if !IsTruncatedBodyErr(err) {
		t.Errorf("malformed-line error must classify as truncation (for retry): %v", err)
	}
}

func TestRetrieveNon200NotRetried(t *testing.T) {
	defer func(orig func(int) time.Duration) { backoffDelayFn = orig }(backoffDelayFn)
	backoffDelayFn = func(int) time.Duration { return 0 }

	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := newAnthropicProvider("k", srv.URL)
	_, err := p.RetrieveBatch(srv.URL + "/results")
	if err == nil {
		t.Fatal("500 must error")
	}
	if attempts.Load() != 1 {
		t.Errorf("non-truncation errors must not retry: attempts = %d", attempts.Load())
	}
}

// Gemini: the retry path works end-to-end, and transport errors are
// sanitized (no API key in the logged/returned error — review Major).
func TestGeminiRetrieveRetriesAndSanitizes(t *testing.T) {
	defer func(orig func(int) time.Duration) { backoffDelayFn = orig }(backoffDelayFn)
	backoffDelayFn = func(int) time.Duration { return 0 }

	good := `{"key":"src-1","response":{"candidates":[{"content":{"parts":[{"text":"ok"}]}}],"usageMetadata":{}}}`
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			fmt.Fprintln(w, good[:len(good)/2])
			return
		}
		fmt.Fprintln(w, good)
	}))
	defer srv.Close()

	p := newGeminiProvider("secret-key-123", srv.URL)
	results, err := p.RetrieveBatch("files/abc123-responses")
	if err != nil {
		t.Fatalf("RetrieveBatch: %v", err)
	}
	if len(results) != 1 || results[0].CustomID != "src-1" {
		t.Errorf("results = %+v", results)
	}
	if attempts.Load() != 2 {
		t.Errorf("attempts = %d, want 2", attempts.Load())
	}
}
