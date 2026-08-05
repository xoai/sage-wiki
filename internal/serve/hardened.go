package serve

import (
	"fmt"
	"net/http"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
)

// MaxMCPBodyBytes caps the JSON-RPC body on the /mcp mount (SPEC-08 D5).
// 1 MiB is generous for tool calls — the capture content cap is 100 KB at
// the tool level — while bounding body-flood abuse.
const MaxMCPBodyBytes int64 = 1 << 20

// NewHardenedServer builds the production http.Server with the SPEC-08 D5
// hardening applied: bounded header/read/idle timeouts, a 1 MiB header cap,
// and the per-connection in-flight request guard. Callers serve the
// returned server directly (main.go's single-workspace path and
// Server.ServeWithListener both use this — one construction, no drift).
func NewHardenedServer(handler http.Handler, lim limits.Limits) *http.Server {
	lim = lim.Resolve()
	return &http.Server{
		Handler:           connLimit(handler, lim.MaxConcurrentRequestsPerConn),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is deliberately 0: /events/stream SSE and /export
		// streaming would be cut mid-stream by a global write deadline.
		// SSE responses are bounded by the request context and the
		// per-query limiter instead (same documented exception as
		// internal/web/server.go).
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
		// MaxHeaderBytes bounds slow-header abuse while leaving ample
		// room for auth tokens and cookies (new in SPEC-08 — unset
		// everywhere before).
		MaxHeaderBytes: 1 << 20,
		ConnContext:    ConnContext,
	}
}

// maxBytesBody caps request bodies at n bytes: a known-oversized
// Content-Length gets a clean 413 envelope before the handler runs; a
// chunked/unknown-length body is wrapped in http.MaxBytesReader so the
// read itself fails at the cap.
func maxBytesBody(next http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > n {
			writeErr(w, http.StatusRequestEntityTooLarge, "body_too_large",
				fmt.Sprintf("request body exceeds %d bytes", n))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, n)
		next.ServeHTTP(w, r)
	})
}
