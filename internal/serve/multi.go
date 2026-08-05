package serve

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
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

	// SPEC-07 (verification pass 3): registry-served event streams,
	// cancelled at shutdown start — an open stream must not pin
	// srv.Shutdown for the whole drain budget.
	sseMu      sync.Mutex
	sseNextID  int64
	sseCancels map[int64]context.CancelFunc
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
		// SPEC-07: the event plane is built at engine-open time (one bus
		// per workspace) and handed to the stack at assembly. GET-OR-
		// CREATE under the registry lock: concurrent/retried opens of one
		// workspace bind the SAME bus, so whichever open wins the Manager
		// race, the shared handle and the served stack always pair on one
		// bus (closing a displaced bus could close the bus a live handle
		// emits into). Bounded: one bus per workspace name. A webhook
		// failure degrades SCOPED (recorded spec deviation): the bus with
		// its audit-trail file sink survives; only the dispatcher is lost,
		// loudly. Workspaces stay open — telemetry never bricks a server.
		engine.WithPerWorkspaceOptions(func(name string) []engine.Option {
			reg.mu.Lock()
			defer reg.mu.Unlock()
			if bus := reg.buses[name]; bus != nil {
				return []engine.Option{engine.WithEventSink(bus)}
			}
			dir := filepath.Join(cfg.Root, name)
			wcfg, err := config.Load(filepath.Join(dir, "config.yaml"))
			if err != nil {
				return nil
			}
			bus, stops, err := BuildEventSurfaces(ctx, dir, wcfg)
			if err != nil {
				slog.Warn("serve: webhooks unavailable — workspace opens with audit trail only",
					"workspace", name, "error", err)
			}
			if bus == nil {
				return nil // events disabled for this workspace
			}
			reg.buses[name] = bus
			reg.busStops[name] = stops
			return []engine.Option{engine.WithEventSink(bus)}
		}),
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
	return &MultiServer{cfg: cfg, mgr: mgr, reg: reg, sem: sem, sseCancels: map[int64]context.CancelFunc{}}, nil
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
// registerSSE tracks an active registry-served stream; unregister removes
// it. A registration that lands while shutdown is cancelling streams is
// cancelled immediately (TOCTOU guard).
func (m *MultiServer) registerSSE(c context.CancelFunc) int64 {
	m.sseMu.Lock()
	defer m.sseMu.Unlock()
	if m.sseCancels == nil { // shutdown already swept the map
		c()
		return -1
	}
	m.sseNextID++
	m.sseCancels[m.sseNextID] = c
	return m.sseNextID
}

func (m *MultiServer) unregisterSSE(id int64) {
	if id < 0 {
		return
	}
	m.sseMu.Lock()
	defer m.sseMu.Unlock()
	delete(m.sseCancels, id)
}

// cancelSSEStreams ends every active registry-served event stream.
func (m *MultiServer) cancelSSEStreams() {
	m.sseMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sseCancels))
	for _, c := range m.sseCancels {
		cancels = append(cancels, c)
	}
	m.sseCancels = nil // nil marks "shutdown swept" for late registrations
	m.sseMu.Unlock()
	for _, c := range cancels {
		c()
	}
}

func (m *MultiServer) routeWorkspace(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/w/")
	name, subPath, _ := strings.Cut(rest, "/")
	if err := engine.ValidateWorkspaceName(name); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "no such workspace")
		return
	}
	// SPEC-07 (verification pass 2): the event stream is served from the
	// REGISTRY-OWNED bus without a stack ref — a stream must not pin the
	// stack (unevictable workspace, shutdown hang). The bus outlives
	// stacks, so the stream survives eviction.
	if subPath == "events/stream" {
		m.reg.mu.Lock()
		bus := m.reg.buses[name]
		m.reg.mu.Unlock()
		if bus == nil {
			writeErr(w, http.StatusServiceUnavailable, "events_disabled", "event stream is not available for this workspace")
			return
		}
		serveEventsStream(w, r, bus, m.registerSSE, m.unregisterSSE)
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
	// SPEC-07 (verification pass 3): end registry-served event streams
	// BEFORE the HTTP drain — a stream never goes idle on its own.
	m.cancelSSEStreams()
	// SPEC-02 drain (multi-workspace): per-stack /mcp sessions hold a stack
	// ref; cancel them too so closeAll→st.close() doesn't block on the
	// refcount wait past the HTTP drain.
	m.reg.cancelStackSSEStreams()
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
