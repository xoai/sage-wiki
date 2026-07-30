package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loopbackReq builds a request whose Host passes the DNS-rebind allowlist.
func loopbackReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:3333"
	return r
}

// Loopback with no token configured stays zero-config (invariant #7), and every
// response carries the CSP.
func TestAuthDisabledLoopback(t *testing.T) {
	srv := setupTestProject(t)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, loopbackReq("GET", "/api/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("loopback tokenless /api/status: want 200, got %d", w.Code)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("missing/weak CSP: %q", csp)
	}
}

func TestAuthRequiredWhenTokenSet(t *testing.T) {
	srv := setupTestProject(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	cases := []struct {
		name string
		auth string // Authorization header ("" = none)
		q    string // ?token= value ("" = none)
		want int
	}{
		{"no credential", "", "", http.StatusUnauthorized},
		{"wrong bearer", "Bearer nope", "", http.StatusUnauthorized},
		{"right bearer", "Bearer s3cret", "", http.StatusOK},
		{"right query token", "", "s3cret", http.StatusOK},
		{"wrong query token", "", "nope", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/status"
			if tc.q != "" {
				target += "?token=" + tc.q
			}
			req := loopbackReq("GET", target)
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s: want %d, got %d", tc.name, tc.want, w.Code)
			}
		})
	}
}

// The static SPA shell must load without a token (it holds no data and must run
// to carry the token to the API).
func TestStaticAssetUnauthenticated(t *testing.T) {
	srv := setupTestProject(t)
	srv.SetAuth("s3cret", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, loopbackReq("GET", "/"))
	if w.Code == http.StatusUnauthorized {
		t.Error("static shell should not require auth, got 401")
	}
}

func TestHostAllowlistRejectsRebind(t *testing.T) {
	srv := setupTestProject(t)
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Host = "evil.example.com" // not loopback, not allowed
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("DNS-rebind host: want 403, got %d", w.Code)
	}
}

func TestHostAllowlistAcceptsConfigured(t *testing.T) {
	srv := setupTestProject(t)
	srv.SetAuth("", []string{"wiki.example.com"})
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.Host = "wiki.example.com"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("allowed host: want 200, got %d", w.Code)
	}
}

// /api/query is stream (SSE) — a foreign Origin must be rejected even though it
// is a GET.
func TestOriginRejectedOnQuery(t *testing.T) {
	srv := setupTestProject(t)
	req := loopbackReq("GET", "/api/query")
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("foreign origin on /api/query: want 403, got %d", w.Code)
	}
}

func TestSVGServedAsAttachment(t *testing.T) {
	srv := setupTestProject(t)
	dir := filepath.Join(srv.projectDir, srv.cfg.Output, "concepts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "x.svg"),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o644)

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, loopbackReq("GET", "/api/files/concepts/x.svg"))
	if w.Code != http.StatusOK {
		t.Fatalf("svg: want 200, got %d", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != "attachment" {
		t.Errorf("svg Content-Disposition: want attachment, got %q", cd)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("svg CSP: want sandbox, got %q", csp)
	}
}

func TestFileRangeRequest(t *testing.T) {
	srv := setupTestProject(t)
	dir := filepath.Join(srv.projectDir, srv.cfg.Output, "concepts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "test.png"), []byte("0123456789"), 0o644)

	req := loopbackReq("GET", "/api/files/concepts/test.png")
	req.Header.Set("Range", "bytes=0-2")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent {
		t.Errorf("range: want 206, got %d", w.Code)
	}
	if body := w.Body.String(); body != "012" {
		t.Errorf("range body: want 012, got %q", body)
	}
}

func TestCheckBindAuth(t *testing.T) {
	cases := []struct {
		bind, token string
		wantErr     bool
	}{
		{"127.0.0.1", "", false},
		{"localhost", "", false},
		{"::1", "", false},
		{"0.0.0.0", "", true},
		{"192.168.1.5", "", true},
		{"0.0.0.0", "tok", false},
		{"", "", true}, // empty bind = all interfaces → token required
		{"", "tok", false},
	}
	for _, tc := range cases {
		if err := CheckBindAuth(tc.bind, tc.token); (err != nil) != tc.wantErr {
			t.Errorf("CheckBindAuth(%q,%q): err=%v want err=%v", tc.bind, tc.token, err, tc.wantErr)
		}
	}
}

// A cross-origin WebSocket upgrade is rejected before the hijack.
func TestWebSocketForeignOriginRejected(t *testing.T) {
	srv := setupTestProject(t)
	req := loopbackReq("GET", "/ws")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Origin", "http://evil.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("WS foreign origin: want 403, got %d", w.Code)
	}
}

// /ws is gated by the token like /api/*.
func TestWebSocketRequiresToken(t *testing.T) {
	srv := setupTestProject(t)
	srv.SetAuth("s3cret", nil)
	req := loopbackReq("GET", "/ws")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("WS without token: want 401, got %d", w.Code)
	}
}

// The HTML shell (served for "/") also carries the CSP.
func TestHTMLShellHasCSP(t *testing.T) {
	srv := setupTestProject(t)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, loopbackReq("GET", "/"))
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("HTML shell missing CSP: %q", csp)
	}
}

// Cancelling the context shuts the server down and Serve returns nil.
func TestGracefulShutdown(t *testing.T) {
	srv := setupTestProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, "127.0.0.1:0") }()

	time.Sleep(100 * time.Millisecond) // let the listener bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after cancel: want nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of shutdown")
	}
}
