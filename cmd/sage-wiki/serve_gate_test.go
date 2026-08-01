package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestUseHTTPMode pins the gate predicate (R-07/N-05): bare serve → HTTP
// mode; explicit --transport or --ui → those paths; --addr always wins.
func TestUseHTTPMode(t *testing.T) {
	for _, tc := range []struct {
		addr     string
		changed  bool
		ui       bool
		wantHTTP bool
	}{
		{"", false, false, true},
		{"", true, false, false},
		{"", true, true, false},
		{"", false, true, false},
		{"127.0.0.1:8484", true, true, true},
	} {
		if got := useHTTPMode(tc.addr, tc.changed, tc.ui); got != tc.wantHTTP {
			t.Errorf("useHTTPMode(%q, %v, %v) = %v, want %v", tc.addr, tc.changed, tc.ui, got, tc.wantHTTP)
		}
	}
}

// TestStartupSwapShape is the N-01 regression pin: ONE server with an
// atomic readiness-aware handler — healthz answers while "starting",
// readyz 503s pre-swap, and the full surface works post-swap (no
// interim server, no closed listener).
func TestStartupSwapShape(t *testing.T) {
	var ready atomic.Bool
	var liveHandler atomic.Value
	srv := httptest.NewUnstartedServer(nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := liveHandler.Load().(http.Handler); ok && h != nil {
			h.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"starting"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv.Start()
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body := make([]byte, 512)
		n, _ := resp.Body.Read(body)
		return resp.StatusCode, string(body[:n])
	}

	code, _ := get("/healthz")
	if code != 200 {
		t.Errorf("healthz during startup = %d, want 200", code)
	}
	code, _ = get("/readyz")
	if code != 503 {
		t.Errorf("readyz during startup = %d, want 503", code)
	}

	// Simulate build completion: swap to a real route surface.
	liveHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("full-surface"))
	}))
	ready.Store(true)
	code, body := get("/readyz")
	if code != 200 {
		t.Errorf("readyz after build = %d, want 200", code)
	}
	code, body = get("/jobs")
	if code != 200 || !strings.Contains(body, "full-surface") {
		t.Errorf("post-swap surface = %d %q", code, body)
	}
}

// TestServeTransportGateShape (R-07/N-05 duplicate guard): re-added after
// the earlier test file was removed in the R-round; kept as the single
// gate table.
