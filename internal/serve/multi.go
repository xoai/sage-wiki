package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/pkg/engine"
)

// MultiConfig is the multi-workspace (--workspace-root) construction
// surface (SPEC-06 §5).
type MultiConfig struct {
	Root                  string
	Tokens                []string // root-level bearer tokens (guard ALL /w/*)
	MaxOpen               int      // LRU bound on live stacks; 0 = unlimited
	IdleClose             time.Duration
	MaxConcurrentCompiles int
	DrainTimeout          time.Duration
	RateLimit             func(next http.Handler) http.Handler
}

// MultiServer is the multi-workspace HTTP server: ONE root listener with
// /v1/workspaces plus /w/{name}/... routed to lazily assembled
// per-workspace stacks (the full SPEC-02 surface per workspace). The
// single-workspace server is untouched by this type.
type MultiServer struct {
	cfg MultiConfig
	mgr *engine.Manager
	reg *stackRegistry
	sem chan struct{} // root compile gate shared by every stack

	srvMu   sync.Mutex
	httpSrv *http.Server
}

// NewMulti builds the root server: Manager over root (LRU + idle close +
// the registry eviction seam) and the root mux.
func NewMulti(ctx context.Context, cfg MultiConfig) (*MultiServer, error) {
	if cfg.MaxConcurrentCompiles < 1 {
		cfg.MaxConcurrentCompiles = 2
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 30 * time.Second
	}
	if cfg.RateLimit == nil {
		cfg.RateLimit = func(next http.Handler) http.Handler { return next }
	}
	reg := newStackRegistry(ctx, cfg.MaxConcurrentCompiles)
	mgrOpts := []engine.ManagerOption{
		engine.WithMaxOpen(cfg.MaxOpen),
		engine.WithOnEvict(reg.evict),
	}
	if cfg.IdleClose > 0 {
		mgrOpts = append(mgrOpts, engine.WithIdleClose(cfg.IdleClose))
	}
	mgr, err := engine.OpenManager(ctx, cfg.Root, mgrOpts...)
	if err != nil {
		return nil, err
	}
	reg.mgr = mgr
	// ONE root-level compile gate (SPEC-06 §5): every stack's queue exec
	// acquires it, so --max-concurrent-compiles bounds compiles ACROSS
	// workspaces. Per-stack queues/ledgers stay per-workspace (recovery
	// semantics unchanged).
	sem := make(chan struct{}, cfg.MaxConcurrentCompiles)
	reg.compileSem = sem
	return &MultiServer{cfg: cfg, mgr: mgr, reg: reg, sem: sem}, nil
}

// Handler returns the root handler (rate-limit slot → root auth → router).
func (m *MultiServer) Handler() http.Handler {
	return m.cfg.RateLimit(m.authMiddleware(http.HandlerFunc(m.route)))
}

// authMiddleware enforces the ROOT token set on all routes except
// /healthz. Per-workspace auth is a SPEC-06 non-goal — operators compose
// tenancy in front; one root token guards every workspace.
func (m *MultiServer) authMiddleware(next http.Handler) http.Handler {
	digests := make([][]byte, 0, len(m.cfg.Tokens))
	for _, t := range m.cfg.Tokens {
		digests = append(digests, tokenDigest(t))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || len(digests) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		presented := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			presented = strings.TrimPrefix(h, "Bearer ")
		} else if q := r.URL.Query().Get("token"); q != "" {
			presented = q
		}
		if !anyTokenMatch(tokenDigest(presented), digests, subtle.ConstantTimeCompare) {
			writeErr(w, http.StatusUnauthorized, "unauthenticated", "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// route dispatches root routes. Written as a plain handler (not ServeMux)
// so the /w/ name segment is validated BEFORE any cleaning/redirect
// behavior — traversal shapes get a flat 404, never a redirect oracle.
func (m *MultiServer) route(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/healthz":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	case path == "/v1/workspaces" && r.Method == http.MethodGet:
		infos, err := m.mgr.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		if infos == nil {
			infos = []engine.WorkspaceInfo{}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(infos)
	case strings.HasPrefix(path, "/w/"):
		m.routeWorkspace(w, r)
	default:
		writeErr(w, http.StatusNotFound, "not_found", r.Method+" "+path+": no such route")
	}
}

// routeWorkspace validates the name segment and delegates to the stack
// with the prefix stripped. Invalid names and unknown workspaces are the
// SAME 404 — the registry is not enumerable through error shapes.
func (m *MultiServer) routeWorkspace(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/w/")
	name, _, _ := strings.Cut(rest, "/")
	if err := engine.ValidateWorkspaceName(name); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such workspace")
		return
	}
	st, err := m.reg.acquire(r.Context(), name)
	if err != nil {
		if errors.Is(err, engine.ErrUnknownWorkspace) || errors.Is(err, engine.ErrInvalidWorkspaceName) {
			writeErr(w, http.StatusNotFound, "not_found", "no such workspace")
			return
		}
		if errors.Is(err, engine.ErrLocked) {
			// A slow first open lost the retry budget (F-056): transient
			// contention, not a server fault — 503 + Retry-After.
			w.Header().Set("Retry-After", "1")
			writeErr(w, http.StatusServiceUnavailable, "busy", "workspace is contended — retry shortly")
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer st.release()
	http.StripPrefix("/w/"+name, st.handler).ServeHTTP(w, r)
}

// Serve binds addr and blocks until ctx is cancelled, then drains:
// root HTTP stop → stacks close (refcount-zero) → Manager lock release.
func (m *MultiServer) Serve(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	m.srvMu.Lock()
	m.httpSrv = &http.Server{
		Handler:           m.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	srv := m.httpSrv
	m.srvMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case err := <-errCh:
		if derr := m.Shutdown(); derr != nil {
			return derr
		}
		return err
	case <-ctx.Done():
		return m.Shutdown()
	}
}

// Shutdown drains: HTTP → stacks → Manager (locks released last).
// Idempotent.
func (m *MultiServer) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.DrainTimeout)
	defer cancel()
	var firstErr error
	m.srvMu.Lock()
	srv := m.httpSrv
	m.srvMu.Unlock()
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := m.reg.closeAll(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := m.mgr.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
