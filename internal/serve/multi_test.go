package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// multiFixture builds a root with two workspaces and a MultiServer over it.
func multiFixture(t *testing.T, tokens []string) *MultiServer {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	for _, n := range []string{"ws-a", "ws-b"} {
		w, err := engine.Init(ctx, filepath.Join(root, n))
		if err != nil {
			t.Fatalf("init %s: %v", n, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	ms, err := NewMulti(ctx, MultiConfig{Root: root, Tokens: tokens, MaxOpen: 2})
	if err != nil {
		t.Fatalf("NewMulti: %v", err)
	}
	t.Cleanup(func() { _ = ms.Shutdown() })
	return ms
}

func TestServeMultiWorkspace(t *testing.T) {
	ms := multiFixture(t, nil)
	h := ms.Handler()

	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if len(headers) > 0 {
			r.Header.Set("Authorization", headers[0])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Workspace listing.
	w := get("/v1/workspaces")
	if w.Code != 200 {
		t.Fatalf("/v1/workspaces = %d", w.Code)
	}
	var infos []engine.WorkspaceInfo
	if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil {
		t.Fatalf("parse workspaces: %v (%s)", err, w.Body.String())
	}
	if len(infos) != 2 {
		t.Errorf("/v1/workspaces returned %d entries, want 2", len(infos))
	}

	// Interleaved stack routes across both workspaces.
	for i := 0; i < 3; i++ {
		for _, name := range []string{"ws-a", "ws-b"} {
			w := get("/w/" + name + "/healthz")
			if w.Code != 200 {
				t.Errorf("/w/%s/healthz = %d, want 200", name, w.Code)
			}
		}
	}

	// Root healthz is open.
	if w := get("/healthz"); w.Code != 200 {
		t.Errorf("root healthz = %d", w.Code)
	}

	// Invalid name segments → 404 (no oracle about valid names).
	for _, bad := range []string{"BAD", "a%20b", "-x", "a%2Fb"} {
		if w := get("/w/" + bad + "/healthz"); w.Code != http.StatusNotFound {
			t.Errorf("/w/%s/healthz = %d, want 404", bad, w.Code)
		}
	}
	// Unknown (but valid) name → 404.
	if w := get("/w/ghost/healthz"); w.Code != http.StatusNotFound {
		t.Errorf("/w/ghost/healthz = %d, want 404", w.Code)
	}
	// Unmatched root path → 404.
	if w := get("/nope"); w.Code != http.StatusNotFound {
		t.Errorf("/nope = %d, want 404", w.Code)
	}
}

func TestServeMultiWorkspace_RootToken(t *testing.T) {
	ms := multiFixture(t, []string{"root-secret"})
	h := ms.Handler()

	// /w/* requires the root token.
	r := httptest.NewRequest("GET", "/w/ws-a/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d, want 401", w.Code)
	}
	r = httptest.NewRequest("GET", "/w/ws-a/healthz", nil)
	r.Header.Set("Authorization", "Bearer root-secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("root token: %d, want 200", w.Code)
	}
	// /v1/workspaces is guarded too.
	r = httptest.NewRequest("GET", "/v1/workspaces", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("/v1/workspaces no token: %d, want 401", w.Code)
	}
	// Root healthz stays open (readiness probes).
	r = httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("root healthz with tokens configured = %d, want 200", w.Code)
	}
}

func TestServeMultiWorkspace_NameValidationInPath(t *testing.T) {
	ms := multiFixture(t, nil)
	h := ms.Handler()
	// Traversal shapes that survive URL decoding: the name segment is
	// validated BEFORE any registry lookup.
	for _, p := range []string{"/w/../healthz", "/w/%2e%2e/healthz", "/w/a/..%2f..%2fetc"} {
		r := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 {
			t.Errorf("%s served (200) — traversal must never reach a workspace", p)
		}
	}
}

func TestNewMulti_BadRoot(t *testing.T) {
	if _, err := NewMulti(context.Background(), MultiConfig{Root: filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Error("NewMulti on missing root must error")
	}
}

// F-040 witness: the MCP half of the multi-workspace criterion — an MCP
// handshake through /w/{name}/mcp reaches the stack's streamable mount.
func TestServeMultiWorkspace_MCPThroughPrefix(t *testing.T) {
	ms := multiFixture(t, nil)
	httpSrv := httptest.NewServer(ms.Handler())
	defer httpSrv.Close()

	ctx := context.Background()
	c, err := client.NewStreamableHttpClient(httpSrv.URL + "/w/ws-a/mcp")
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.Close()
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "multi-test", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("initialize through /w/ws-a/mcp: %v", err)
	}
	tools, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("list tools through prefix: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Error("no tools through /w/ws-a/mcp — the prefix route did not reach the stack's MCP mount")
	}
}

// TestMultiBusPairingUnderRace (SPEC-07 wiring): four racing cold opens
// of one workspace must pair the shared engine handle and the served stack
// on ONE registry-owned bus. Ownership-take designs could hand the bus to
// a racer that loses the insertion race and close it there; the registry-
// owned model cannot — this race asserts the pairing survives.
func TestMultiBusPairingUnderRace(t *testing.T) {
	ms := multiFixture(t, nil)
	h := ms.Handler()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/w/ws-a/healthz", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
		}()
	}
	wg.Wait()

	ms.reg.mu.Lock()
	st := ms.reg.stacks["ws-a"]
	regBus := ms.reg.buses["ws-a"]
	ms.reg.mu.Unlock()

	if st == nil {
		t.Fatal("no stack for ws-a after the race")
	}
	// Registry-owned model: the bus persists per workspace name; the
	// stack references it and serves THAT bus — whichever open won the
	// Manager race, handle and served surface pair on one plane.
	if regBus == nil {
		t.Fatal("no bus registered for ws-a after the race")
	}
	if st.bus != regBus {
		t.Error("the stack's bus reference is not the registered bus")
	}
	if st.srv.cfg.Bus != regBus {
		t.Error("the served bus is not the registered bus — engine events would miss the served surfaces")
	}
}
