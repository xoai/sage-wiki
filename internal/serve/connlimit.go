package serve

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
)

// connCtxKey is the context key under which ConnContext stores the
// per-connection in-flight counter (SPEC-08 D5).
type connCtxKey struct{}

// connInFlight counts the requests currently in flight on one connection.
type connInFlight struct {
	n int64 // atomic
}

// ConnContext is the http.Server.ConnContext hook: every accepted
// connection gets its own in-flight counter in the request context. The
// connLimit middleware reads it to enforce
// limits.MaxConcurrentRequestsPerConn.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connCtxKey{}, &connInFlight{})
}

// connLimit rejects with a 429 envelope when a connection has `cap`
// requests already in flight. HTTP/1.1 without TLS is sequential by
// construction, so the default cap (8) is purely defensive — the guard
// covers hijack/upgrade edge cases (SPEC-08 D5). A request with no
// per-connection counter (direct dispatch in tests) passes through.
func connLimit(next http.Handler, cap int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ci, _ := r.Context().Value(connCtxKey{}).(*connInFlight)
		if ci == nil {
			next.ServeHTTP(w, r)
			return
		}
		if atomic.AddInt64(&ci.n, 1) > cap {
			atomic.AddInt64(&ci.n, -1)
			writeErr(w, http.StatusTooManyRequests, "too_many_requests",
				"connection has too many requests in flight")
			return
		}
		defer atomic.AddInt64(&ci.n, -1)
		next.ServeHTTP(w, r)
	})
}
