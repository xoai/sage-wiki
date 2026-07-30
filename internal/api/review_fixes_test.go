package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F-017 witness: 500s from write tools must not leak absolute paths.
func TestDispatch_WriteToolError_DoesNotLeakPaths(t *testing.T) {
	_, mux, dir := newTestRouter(t, nil)
	// Make the output path unusable so the tool's MkdirAll fails with an
	// absolute path in its message (a FILE named "wiki" blocks dir creation;
	// works even when tests run as root, unlike chmod).
	wikiPath := filepath.Join(dir, "wiki")
	if err := os.RemoveAll(wikiPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wikiPath, []byte("not a dir"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		method, target, body string
	}{
		{"PUT", "/v1/summaries", `{"source":"raw/x.md","content":"y"}`},
		{"PUT", "/v1/articles/some-concept", `{"content":"y"}`},
	} {
		w := serve(t, mux, tc.method, tc.target, tc.body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s %s → %d, want 500", tc.method, tc.target, w.Code)
			continue
		}
		if strings.Contains(w.Body.String(), dir) {
			t.Errorf("%s %s leaks project path: %s", tc.method, tc.target, w.Body.String())
		}
		if errCode(t, w) != "internal" {
			t.Errorf("%s %s code = %s, want internal", tc.method, tc.target, errCode(t, w))
		}
	}
}

// F-018 witness: a panicking handler must not wedge the idempotency key.
func TestIdempotency_LeaderPanicDoesNotWedgeKey(t *testing.T) {
	r := &Router{d: nil, idem: newIdemStore()}
	panicky := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		panic("boom")
	})
	h := r.idempotent(panicky)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate after cleanup")
			}
		}()
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "panic-key")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// The key must not be wedged: a follower returns immediately (no hang)
	// with the stored 500 envelope.
	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("POST", "/v1/learnings", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "panic-key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusInternalServerError {
			t.Fatalf("follow-up → %d, want the stored 500", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("follow-up request hung — the key is wedged")
	}
}

// F-019 witness: store is a true LRU — reads and re-stores refresh position.
func TestIdemStore_LRURefresh(t *testing.T) {
	s := newIdemStore()
	put := func(k string) {
		c, leader := s.begin(k)
		if !leader {
			t.Fatalf("%s unexpectedly in flight", k)
		}
		s.finish(k, c, idemEntry{status: 200, stored: s.now()})
	}
	put("a")
	put("b")
	put("c")
	if _, ok := s.get("a"); !ok {
		t.Fatal("a should be stored")
	}
	// After reading a, eviction order must be b, c, a.
	s.mu.Lock()
	got := append([]string{}, s.order...)
	s.mu.Unlock()
	if got[0] != "b" || got[2] != "a" {
		t.Fatalf("order after get(a) = %v, want [b c a]", got)
	}
	// Re-storing an existing key refreshes it too.
	c, leader := s.begin("b")
	if !leader {
		t.Fatal("b unexpectedly in flight")
	}
	s.finish("b", c, idemEntry{status: 200, stored: s.now()})
	s.mu.Lock()
	got = append([]string{}, s.order...)
	s.mu.Unlock()
	if got[0] != "c" || got[2] != "b" {
		t.Fatalf("order after re-store b = %v, want [c a b]", got)
	}
}

// F-023 witness: expired entries are reclaimed on read.
func TestIdemStore_ExpiredEntryReclaimed(t *testing.T) {
	s := newIdemStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	c, _ := s.begin("old")
	s.finish("old", c, idemEntry{status: 200, stored: now})
	s.now = func() time.Time { return now.Add(idemTTL + time.Hour) }
	if _, ok := s.get("old"); ok {
		t.Fatal("expired entry should miss")
	}
	s.mu.Lock()
	n := len(s.entries)
	m := len(s.order)
	s.mu.Unlock()
	if n != 0 || m != 0 {
		t.Fatalf("expired entry not reclaimed: entries=%d order=%d", n, m)
	}
}

// F-020 witness: commit with NO body is accepted (message optional).
func TestGitCommit_NoBody(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	req := httptest.NewRequest("POST", "/v1/git/commit", http.NoBody)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("commit with no body → %d (%s), want 200 (message optional)", w.Code, w.Body.String())
	}
}

// F-021 witness: unmatched /v1 paths get the envelope, not ServeMux's text.
func TestRouter_UnmatchedPathsEnvelope(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "GET", "/v1/nope", "")
	if w.Code != 404 || errCode(t, w) != "not_found" {
		t.Errorf("GET /v1/nope → %d %q, want 404 not_found envelope", w.Code, w.Body.String())
	}
	w = serve(t, mux, "DELETE", "/v1/search", "")
	if w.Code != 405 || errCode(t, w) != "invalid_argument" {
		t.Errorf("DELETE /v1/search → %d %q, want 405 envelope", w.Code, w.Body.String())
	}
	// Multi-segment wildcard routes must also 405, not 404.
	w = serve(t, mux, "DELETE", "/v1/articles/a/b", "")
	if w.Code != 405 || errCode(t, w) != "invalid_argument" {
		t.Errorf("DELETE /v1/articles/a/b → %d %q, want 405 envelope", w.Code, w.Body.String())
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("Allow = %q, want GET", allow)
	}
	w = serve(t, mux, "DELETE", "/v1/articles/some-concept", "")
	if w.Code != 405 {
		t.Errorf("DELETE /v1/articles/some-concept → %d, want 405", w.Code)
	}
	if allow := w.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "PUT") {
		t.Errorf("Allow = %q, want GET+PUT", allow)
	}
}

// F-024 witness: fractional numeric args are rejected.
func TestGraphQuery_FractionalNumbersRejected(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	w := serve(t, mux, "POST", "/v1/graph/query", `{"question":"q","hops":2.5}`)
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("hops 2.5 → %d %s, want 400 invalid_argument", w.Code, errCode(t, w))
	}
	w = serve(t, mux, "GET", "/v1/ontology/a/traverse?depth=2.5", "")
	if w.Code != 400 || errCode(t, w) != "invalid_argument" {
		t.Errorf("depth 2.5 → %d %s, want 400 invalid_argument", w.Code, errCode(t, w))
	}
}

// F-027 witness: Idempotency-Key on a GET is ignored.
func TestIdempotency_KeyIgnoredOnGET(t *testing.T) {
	_, mux, _ := newTestRouter(t, nil)
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Idempotency-Key", "get-key")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET with key → %d", w.Code)
	}
	if w.Header().Get("X-Idempotent-Replay") != "" {
		t.Fatal("GET must never carry the replay header")
	}
}
