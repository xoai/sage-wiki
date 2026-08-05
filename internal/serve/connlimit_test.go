package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
)

// connCtxRequest builds a request carrying the per-connection context
// ConnContext would install on a real connection.
func connCtxRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(ConnContext(r.Context(), nil))
}

// TestConnLimitAllowsUnderCap: requests at or under the cap all reach the
// handler.
func TestConnLimitAllowsUnderCap(t *testing.T) {
	var seen int
	h := connLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		w.WriteHeader(200)
	}), 2)
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, connCtxRequest(t, "GET", "/"))
		if w.Code != 200 {
			t.Fatalf("request %d = %d, want 200", i, w.Code)
		}
	}
	if seen != 3 {
		t.Errorf("handler ran %d times, want 3", seen)
	}
}

// TestConnLimitRejectsOverCap: while one request is in flight on the
// connection, a second one over a cap of 1 gets the 429 envelope.
func TestConnLimitRejectsOverCap(t *testing.T) {
	block := make(chan struct{})
	first := make(chan struct{})
	var calls int
	h := connLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			first <- struct{}{}
			<-block
		}
		w.WriteHeader(200)
	}), 1)

	ctx := ConnContext(context.Background(), nil)
	r1 := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), r1)
		close(done)
	}()
	<-first

	r2 := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("over-cap request = %d, want 429", w2.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &env); err != nil {
		t.Fatalf("429 body is not an envelope: %v: %s", err, w2.Body.String())
	}
	if env.Error.Code == "" {
		t.Error("429 envelope has no error.code")
	}
	close(block)
	<-done

	// After the first request finishes the connection is usable again.
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if w3.Code != 200 {
		t.Errorf("post-drain request = %d, want 200", w3.Code)
	}
}

// TestConnLimitWithoutConnContextPassesThrough: a request with no
// per-connection counter (httptest direct dispatch) is never rejected.
func TestConnLimitWithoutConnContextPassesThrough(t *testing.T) {
	h := connLimit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}), 1)
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 200 {
			t.Fatalf("request %d = %d, want 200 (no conn context = no cap)", i, w.Code)
		}
	}
}

// TestConnLimitResolvesFromLimits: the cap comes from the resolved limits —
// an unset Limits uses the package default, never zero.
func TestConnLimitResolvesFromLimits(t *testing.T) {
	if got := (limits.Limits{}).Resolve().MaxConcurrentRequestsPerConn; got <= 0 {
		t.Fatal("resolved per-conn cap must be positive")
	}
}
