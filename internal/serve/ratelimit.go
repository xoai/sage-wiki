package serve

import (
	"net"
	"net/http"
	"sync"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// mountMCP registers the streamable HTTP MCP endpoint at /mcp behind the
// server's auth middleware (the SDK manages per-connection sessions).
// Returns the streamable server for the drain sequence's Shutdown.
// Nil-safe: a server built without an MCP backend (tests) skips the mount.
func (s *Server) mountMCP() *mcpserver.StreamableHTTPServer {
	if s.mcp == nil {
		return nil
	}
	sh := mcpserver.NewStreamableHTTPServer(s.mcp.MCPServer(),
		mcpserver.WithEndpointPath("/mcp"))
	// SPEC-08 D5: JSON-RPC bodies are capped at the mount (a flood of
	// oversized bodies must not reach the MCP SDK).
	capped := maxBytesBody(sh, MaxMCPBodyBytes)
	// SPEC-02 drain: track long-lived /mcp SSE sessions so the shutdown
	// sweep can cancel them (otherwise a connected client pins the drain).
	tracked := s.trackForShutdown(capped)
	s.mux.Handle("/mcp", tracked)
	s.mux.Handle("/mcp/", tracked)
	return sh
}

// TokenBucket is the EXAMPLE rate-limit middleware (stdlib only) — policy
// belongs with the operator; this is the hook's reference shape (spec §2.6).
type TokenBucket struct {
	rate  float64
	burst float64
	mu    sync.Mutex
	toks  map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewTokenBucket admits `burst` at once then refills at `rate`/second per IP.
func NewTokenBucket(rate, burst float64) *TokenBucket {
	return &TokenBucket{rate: rate, burst: burst, toks: map[string]*bucket{}}
}

// Middleware implements Config.RateLimit's shape.
func (tb *TokenBucket) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tb.allow(clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (tb *TokenBucket) allow(ip string) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	// Evict buckets idle >10m — an internet-facing example must not grow
	// one map entry per client IP forever (F-056).
	for k, v := range tb.toks {
		if time.Since(v.at) > 10*time.Minute {
			delete(tb.toks, k)
		}
	}
	b, ok := tb.toks[ip]
	if !ok {
		b = &bucket{tokens: tb.burst, at: time.Now()}
		tb.toks[ip] = b
	}
	elapsed := time.Since(b.at).Seconds()
	b.tokens += elapsed * tb.rate
	if b.tokens > tb.burst {
		b.tokens = tb.burst
	}
	b.at = time.Now()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
