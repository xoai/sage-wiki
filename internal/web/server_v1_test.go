package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/config"
	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
)

// setupV1 wires the /v1 facade into a test WebServer exactly as
// cmd/sage-wiki does: a real MCP server (shared coordinator semantics
// aside) behind api.New, mounted via SetV1Handler.
func setupV1(t *testing.T) *WebServer {
	t.Helper()
	srv := setupTestProject(t)
	dir := srv.projectDir
	mcpSrv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatalf("mcp.NewServer: %v", err)
	}
	t.Cleanup(func() { mcpSrv.Close() })
	cfg, err := config.Load(dir + "/config.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv.SetV1Handler(api.New(mcpSrv, cfg, dir, nil).Handler())
	return srv
}

func TestV1_AuthMatrix(t *testing.T) {
	srv := setupV1(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	cases := []struct {
		name string
		auth string
		want int
		code string // expected error code in the /v1 envelope ("" for 200)
	}{
		{"no credential", "", http.StatusUnauthorized, "unauthenticated"},
		{"wrong bearer", "Bearer nope", http.StatusUnauthorized, "unauthenticated"},
		{"right bearer", "Bearer s3cret", http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := loopbackReq("GET", "/v1/status")
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("/v1/status → %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("401/403 body is not the /v1 envelope: %s", w.Body.String())
			}
		})
	}

	t.Run("foreign host is 403 envelope", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v1/status", nil)
		req.Host = "evil.com"
		req.Header.Set("Authorization", "Bearer s3cret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("foreign host → %d, want 403", w.Code)
		}
		if !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
			t.Fatalf("403 body is not the /v1 envelope: %s", w.Body.String())
		}
	})
}

func TestV1_LoopbackZeroConfig(t *testing.T) {
	srv := setupV1(t)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, loopbackReq("GET", "/v1/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("loopback tokenless /v1/status: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"project"`) {
		t.Fatalf("structured status expected: %s", w.Body.String())
	}
}

// The origin check covers state-changing /v1 methods (POST and PUT) with
// the envelope; a matching Origin passes.
func TestV1_OriginCheck(t *testing.T) {
	srv := setupV1(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	for _, tc := range []struct {
		method, target, body string
	}{
		{"POST", "/v1/learnings", `{"type":"gotcha","content":"x"}`},
		{"PUT", "/v1/summaries", `{"source":"a","content":"b"}`},
	} {
		req := loopbackReq(tc.method, tc.target)
		req.Header.Set("Authorization", "Bearer s3cret")
		req.Header.Set("Origin", "https://evil.example")
		req.Body = io.NopCloser(strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
			t.Errorf("%s %s with foreign Origin → %d %q, want 403 envelope", tc.method, tc.target, w.Code, w.Body.String())
		}
	}

	req := loopbackReq("PUT", "/v1/summaries")
	req.Header.Set("Authorization", "Bearer s3cret")
	req.Header.Set("Origin", "http://127.0.0.1:3333")
	req.Body = io.NopCloser(strings.NewReader(`{"source":"a","content":"b"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Errorf("matching Origin rejected: %s", w.Body.String())
	}
}

// The prime directive: /api/* is byte-unchanged. Its 401 stays the plain
// text the SPA expects; only /v1 gets the envelope.
func TestV1_APIRoutesUnchanged(t *testing.T) {
	srv := setupV1(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, loopbackReq("GET", "/api/status"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless /api/status → %d, want 401", w.Code)
	}
	if strings.Contains(w.Body.String(), `"error"`) {
		t.Fatalf("/api/* 401 changed shape: %s", w.Body.String())
	}

	srv2 := setupV1(t)
	w2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(w2, loopbackReq("GET", "/api/status"))
	if w2.Code != http.StatusOK {
		t.Fatalf("loopback tokenless /api/status → %d, want 200", w2.Code)
	}
}

// Idempotency through the mounted Handler(): middleware composed with
// auth/security — same key twice on a write replays byte-identically with
// no second dispatch.
func TestV1_IdempotencyThroughMountedHandler(t *testing.T) {
	srv := setupV1(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	var bodies [2]string
	for i := 0; i < 2; i++ {
		req := loopbackReq("POST", "/v1/learnings")
		req.Header.Set("Authorization", "Bearer s3cret")
		req.Header.Set("Idempotency-Key", "through-handler-key")
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(strings.NewReader(`{"type":"gotcha","content":"via mounted handler"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d → %d (%s)", i, w.Code, w.Body.String())
		}
		if i == 1 && w.Header().Get("X-Idempotent-Replay") != "true" {
			t.Fatalf("second request missing replay header: %v", w.Header())
		}
		bodies[i] = w.Body.String()
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("replays differ:\n%s\n%s", bodies[0], bodies[1])
	}
}

// An unauthenticated failure happens BEFORE the idempotency middleware —
// a 401 must never be stored under a key; the authenticated retry must
// dispatch fresh.
func TestV1_UnauthorizedNotStoredUnderKey(t *testing.T) {
	srv := setupV1(t)
	srv.SetAuth("s3cret", nil)
	h := srv.Handler()

	mk := func(auth string) *httptest.ResponseRecorder {
		req := loopbackReq("POST", "/v1/learnings")
		req.Header.Set("Idempotency-Key", "auth-then-retry")
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(strings.NewReader(`{"type":"gotcha","content":"x"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	if w := mk(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no cred → %d, want 401", w.Code)
	}
	w := mk("Bearer s3cret")
	if w.Code != http.StatusOK {
		t.Fatalf("authed retry → %d (%s), want a fresh 200 dispatch", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Idempotent-Replay") == "true" {
		t.Fatal("the 401 was replayed — middleware order stored an auth failure")
	}
}

func TestV1_ToolSetImmutability(t *testing.T) {
	srv := setupTestProject(t)
	mcpSrv, err := wikimcp.NewServer(srv.projectDir)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()

	want := []string{
		"wiki_search", "wiki_read", "wiki_status", "wiki_ontology_query",
		"wiki_graph_query", "wiki_list", "wiki_provenance", "wiki_add_source",
		"wiki_write_summary", "wiki_write_article", "wiki_add_ontology",
		"wiki_learn", "wiki_commit", "wiki_compile_diff", "wiki_capture",
		"wiki_compile_topic", "wiki_compile", "wiki_lint", "wiki_query",
	}
	tools := mcpSrv.MCPServer().ListTools()
	if len(tools) != 19 {
		t.Fatalf("tool count = %d, want 19", len(tools))
	}
	for _, name := range want {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q missing from registry", name)
		}
	}
}
