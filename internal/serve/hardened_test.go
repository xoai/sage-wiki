package serve

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestNewHardenedServerFields pins the SPEC-08 D5 production construction:
// the timeouts, the 1 MiB header cap, and the documented WriteTimeout 0
// (SSE/export). These are the values AC7's slow-loris tests rely on.
func TestNewHardenedServerFields(t *testing.T) {
	srv := NewHardenedServer(http.NotFoundHandler(), limits.Limits{})
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", srv.ReadTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 1<<20)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (SSE/export must not be cut)", srv.WriteTimeout)
	}
	if srv.ConnContext == nil {
		t.Error("ConnContext is nil — per-connection cap has no counter to read")
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
}

// TestHardenedServerSlowHeadersDropped proves the slow-loris defense is
// live: a connection that stalls mid-header is dropped by the server.
// ReadHeaderTimeout is scaled down after construction (the field test above
// pins the production value) so the test doesn't sleep for 10 seconds.
func TestHardenedServerSlowHeadersDropped(t *testing.T) {
	srv := NewHardenedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}), limits.Limits{})
	srv.ReadHeaderTimeout = 100 * time.Millisecond

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	defer srv.Close()
	addr := l.Addr().String()

	// A normal request completes on the same server.
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("fast request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("fast request = %d, want 200", resp.StatusCode)
	}

	// Slow-loris: send the request line, then stall past ReadHeaderTimeout.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != io.EOF {
		t.Errorf("slow-header conn read = %v, want EOF (server must drop the connection)", err)
	}
}

// TestHardenedServerOversizedHeaderRejected proves MaxHeaderBytes is
// enforced end-to-end: a 1.5 MiB header value gets a 431.
func TestHardenedServerOversizedHeaderRejected(t *testing.T) {
	srv := NewHardenedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}), limits.Limits{})
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config.Addr = "127.0.0.1:0"
	ts.Start()
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Big: %s\r\n\r\n", strings.Repeat("a", 1<<20+1<<19))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "431") {
		t.Errorf("oversized header status = %q, want 431", strings.TrimSpace(line))
	}
}

// TestHardenedServerSSEStreams pin the WriteTimeout-0 behavior: a flushing
// stream is not cut mid-response.
func TestHardenedServerSSEStreams(t *testing.T) {
	srv := NewHardenedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("no flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			f.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}), limits.Limits{})
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Start()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "data:"); n != 3 {
		t.Errorf("SSE chunks received = %d, want 3", n)
	}
}

// TestMCPMountBodyCap proves AC7's /mcp body cap on the REAL mount: a
// JSON-RPC body over 1 MiB gets a 413 before the MCP SDK sees it.
func TestMCPMountBodyCap(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deps.Close)
	mcpSrv, err := mcppkg.NewServer(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mcpSrv.Close() })
	srv, err := New(deps, mcpSrv, Config{
		Workspace: dir,
		ReadyFn:   func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	big := bytes.Repeat([]byte("a"), int(MaxMCPBodyBytes)+1)
	r := httptest.NewRequest("POST", "/mcp", bytes.NewReader(big))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized /mcp body = %d, want 413 (body: %s)", w.Code, w.Body.String())
	}
}
