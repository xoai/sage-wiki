package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// idemOK returns a handler that counts dispatches through the spy and
// returns a prose result, exercising the middleware end to end.
func idemOKHandler(spy *spyDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		dispatch(req.Context(), w, spy, ToolLearn, map[string]any{"type": "gotcha", "content": "x"}, "result")
	}
}

func TestIdempotency_SameKeyReplaysVerbatim(t *testing.T) {
	spy := &spyDispatcher{result: textRes("Learning stored: [gotcha] x")}
	r := &Router{d: spy, idem: newIdemStore()}
	h := r.idempotent(idemOKHandler(spy))

	var bodies [2]string
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{"type":"gotcha","content":"x"}`))
		req.Header.Set("Idempotency-Key", "k-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d → %d", i, w.Code)
		}
		bodies[i] = w.Body.String()
	}
	if spy.calls != 1 {
		t.Fatalf("dispatches = %d, want 1", spy.calls)
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("replays differ:\n%s\n%s", bodies[0], bodies[1])
	}
}

func TestIdempotency_NoKeyAlwaysDispatches(t *testing.T) {
	spy := &spyDispatcher{result: textRes("ok")}
	r := &Router{d: spy, idem: newIdemStore()}
	h := r.idempotent(idemOKHandler(spy))
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
		h.ServeHTTP(w, req)
	}
	if spy.calls != 2 {
		t.Fatalf("dispatches = %d, want 2", spy.calls)
	}
}

func TestIdempotency_DifferentKeysDispatchSeparately(t *testing.T) {
	spy := &spyDispatcher{result: textRes("ok")}
	r := &Router{d: spy, idem: newIdemStore()}
	h := r.idempotent(idemOKHandler(spy))
	for _, k := range []string{"a", "b"} {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", k)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}
	if spy.calls != 2 {
		t.Fatalf("dispatches = %d, want 2", spy.calls)
	}
}

func TestIdempotency_ConcurrentSameKeySingleDispatch(t *testing.T) {
	spy := &spyDispatcher{result: textRes("ok")}
	r := &Router{d: spy, idem: newIdemStore()}
	h := r.idempotent(idemOKHandler(spy))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
			req.Header.Set("Idempotency-Key", "race-key")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if spy.calls != 1 {
		t.Fatalf("dispatches = %d, want 1 (in-flight dedup)", spy.calls)
	}
}

func TestIdemStore_EvictionAtCap(t *testing.T) {
	s := newIdemStore()
	put := func(k string) {
		c, leader := s.begin(k)
		if !leader {
			t.Fatalf("%s unexpectedly in flight", k)
		}
		s.finish(k, c, idemEntry{status: 200, stored: s.now()})
	}
	put("oldest")
	for i := 0; i < idemMaxEntries; i++ {
		put(strings.Repeat("k", 1) + string(rune('a'+i%26)) + strings.Repeat("x", i/26))
	}
	s.mu.Lock()
	n := len(s.entries)
	_, oldestPresent := s.entries["oldest"]
	s.mu.Unlock()
	if n != idemMaxEntries {
		t.Fatalf("entries = %d, want capped at %d", n, idemMaxEntries)
	}
	if oldestPresent {
		t.Fatal("least-recently-used key should have been evicted at cap")
	}
}

func TestIdempotency_ReplaysErrorResponses(t *testing.T) {
	// A 4xx from edge validation inside the wrapped handler is also stored
	// — the client retried the identical request, it gets the identical
	// answer.
	spy := &spyDispatcher{result: errRes("store failed: locked")}
	r := &Router{d: spy, idem: newIdemStore()}
	h := r.idempotent(idemOKHandler(spy))
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "err-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 500 {
			t.Fatalf("request %d → %d, want 500", i, w.Code)
		}
	}
	if spy.calls != 1 {
		t.Fatalf("dispatches = %d, want 1", spy.calls)
	}
}
