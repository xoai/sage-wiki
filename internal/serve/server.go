package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/export"
	"github.com/xoai/sage-wiki/internal/limits"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/pkg/engine"
	"github.com/xoai/sage-wiki/pkg/events"
	"net"
)

// Config is the server's construction surface (spec §2.2).
type Config struct {
	Workspace             string
	Tokens                []string
	MaxConcurrentCompiles int
	DrainTimeout          time.Duration
	RateLimit             func(next http.Handler) http.Handler
	Addr                  string
	// CompileSem, when non-nil, is a SHARED cross-server compile gate
	// (SPEC-06 multi-workspace): the queue exec acquires it around every
	// compile, bounding concurrency across all stacks, not just this one.
	CompileSem chan struct{}
	// ReadyFn reports store-open completion for /readyz. Required.
	ReadyFn func() bool
	// Bus, when non-nil, serves GET /events/stream (SPEC-07 SSE). Nil =
	// the route returns 503 (events disabled).
	Bus *events.Bus
}

// Server is the SPEC-02 unified serve process.
type Server struct {
	cfg       Config
	mcp       *mcppkg.Server
	deps      *Deps
	queue     *Queue
	ledger    *Ledger
	mux       *http.ServeMux
	mcpStream *mcpserver.StreamableHTTPServer
	ws        *engine.Workspace // held for the workspace lock (§2.0)
	srvMu     sync.Mutex        // guards httpSrv (Serve writes, Shutdown reads)
	// SPEC-07 (verification pass 2): active SSE streams, cancelled at
	// shutdown start — an open stream must not consume the whole drain
	// budget (http.Server.Shutdown waits for it) and starve the job
	// queue's drain.
	sseMu      sync.Mutex
	sseNextID  int64
	sseCancels map[int64]context.CancelFunc
	httpSrv    *http.Server
	// lim is the workspace's resolved SPEC-08 limits (config or defaults);
	// ServeWithListener builds the hardened server from it.
	lim limits.Limits
}

// New builds the server: job ledger + queue, routes, MCP mount.
func New(deps *Deps, mcpSrv *mcppkg.Server, cfg Config) (*Server, error) {
	if cfg.MaxConcurrentCompiles < 1 {
		cfg.MaxConcurrentCompiles = 2
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 30 * time.Second // spec default (Q-9)
	} else if cfg.DrainTimeout < 10*time.Second {
		fmt.Fprintf(os.Stderr, "warning: --drain-timeout %v clamped to 10s (minimum)\n", cfg.DrainTimeout)
		cfg.DrainTimeout = 10 * time.Second
	}
	if cfg.RateLimit == nil {
		cfg.RateLimit = func(next http.Handler) http.Handler { return next }
	}
	ledger, err := OpenLedger(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, mcp: mcpSrv, deps: deps, ledger: ledger, sseCancels: map[int64]context.CancelFunc{}}
	s.queue = NewQueue(ledger, cfg.MaxConcurrentCompiles, semaphoreWrap(cfg.CompileSem, s.execCompile), nil)
	s.routes()
	s.mcpStream = s.mountMCP()
	// /v1 stays live on this listener too (Q-3): the existing facade over
	// the same MCP dispatch + the deps job runner.
	apiCfg, err := config.Load(filepath.Join(cfg.Workspace, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("serve: load config for /v1: %w", err)
	}
	s.lim = apiCfg.Limits.Resolve()
	s.mux.Handle("/v1/", api.New(mcpSrv, apiCfg, cfg.Workspace, NewJobRunner(deps, mcpSrv), deps.Progress()).Handler())
	return s, nil
}

// Queue exposes the job queue (the drain sequence stops it).
func (s *Server) Queue() *Queue { return s.queue }

// SetWorkspace attaches the engine workspace held for the lock (§2.0).
func (s *Server) SetWorkspace(w *engine.Workspace) { s.ws = w }

// ClearWorkspace detaches the engine workspace — Shutdown will NOT close
// it. Used when the handle is owned elsewhere (SPEC-06: the Manager owns
// the shared handle; a duplicate stack teardown must never close it,
// F-034).
func (s *Server) ClearWorkspace() { s.ws = nil }

// InjectHTTPServer hands an externally created http.Server to the drain
// sequence (used when the caller serves with a readiness-aware handler —
// N-01: one listener, one server, handler swapped atomically at handoff).
func (s *Server) InjectHTTPServer(h *http.Server) {
	s.srvMu.Lock()
	s.httpSrv = h
	s.srvMu.Unlock()
}

// ServeWithListener serves on a pre-bound listener (early bind, AC-S1).
func (s *Server) ServeWithListener(ctx context.Context, l net.Listener) error {
	s.srvMu.Lock()
	s.httpSrv = NewHardenedServer(s.Handler(), s.lim)
	s.srvMu.Unlock()
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpSrv.Serve(l); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	s.StartQueue(ctx)
	select {
	case err := <-errCh:
		// Bind/serve failure still drains (spec §2.7: bind failure after
		// acquisition releases immediately).
		if derr := s.Shutdown(); derr != nil {
			return derr
		}
		return err
	case <-ctx.Done():
		return s.Shutdown()
	}
}

// Serve binds addr and blocks until ctx is cancelled (SIGTERM path),
// then drains per spec §2.7.
func (s *Server) Serve(ctx context.Context, addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.ServeWithListener(ctx, l)
}

// Shutdown executes the drain sequence (spec §2.7): stop accepting →
// http drain → job queue drain → MCP shutdown → metrics snapshot →
// deps close → lock released LAST.
func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DrainTimeout)
	defer cancel()
	// SPEC-07 (verification pass 2): end SSE streams BEFORE the HTTP
	// drain — Shutdown waits for active connections, and a stream never
	// goes idle on its own; leaving one open would consume the entire
	// drain budget and leave the job queue an expired ctx.
	s.cancelSSEStreams()
	var firstErr error
	s.srvMu.Lock()
	httpSrv := s.httpSrv
	s.srvMu.Unlock()
	if httpSrv != nil {
		if err := httpSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := s.queue.Stop(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if s.mcpStream != nil {
		if err := s.mcpStream.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	metrics.LogSnapshot()
	if s.deps != nil {
		s.deps.Close()
	}
	if s.ws != nil {
		if err := s.ws.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Handler returns the root handler (rate-limit slot → auth → mux).
func (s *Server) Handler() http.Handler {
	return s.cfg.RateLimit(s.authMiddleware(s.mux))
}

// StartQueue launches the FIFO worker.
func (s *Server) StartQueue(ctx context.Context) {
	go s.queue.Run(ctx)
}

func (s *Server) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("POST /capture", s.handleCapture)
	s.mux.HandleFunc("POST /search", s.handleSearch)
	s.mux.HandleFunc("POST /compile", s.handleCompile)
	s.mux.HandleFunc("GET /jobs/{id}", s.handleJobGet)
	s.mux.HandleFunc("GET /jobs", s.handleJobList)
	s.mux.HandleFunc("GET /graph/query", s.handleGraphQuery)
	s.mux.HandleFunc("GET /docs/{path...}", s.handleDoc)
	s.mux.HandleFunc("GET /export", s.handleExport)
	s.mux.Handle("GET /metrics", metrics.Handler())
	s.mux.HandleFunc("GET /events/stream", s.handleEventsStream)
	// Envelope 404 for unmatched paths (spec §2.2 error contract).
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found", r.Method+" "+r.URL.Path+": no such route")
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// cancelSSEStreams ends every active event stream (shutdown start). The
// nil map marks "shutdown swept" so late registrations cancel themselves.
func (s *Server) cancelSSEStreams() {
	s.sseMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.sseCancels))
	for _, c := range s.sseCancels {
		cancels = append(cancels, c)
	}
	s.sseCancels = nil
	s.sseMu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// registerSSE tracks an active stream; unregister removes it by id. A
// registration that lands after cancelSSEStreams swept the map is cancelled
// immediately (TOCTOU guard — verification pass 3): a late connection must
// not outlive the sweep and pin the HTTP drain.
func (s *Server) registerSSE(c context.CancelFunc) int64 {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	if s.sseCancels == nil { // shutdown already swept the map
		c()
		return -1
	}
	s.sseNextID++
	s.sseCancels[s.sseNextID] = c
	return s.sseNextID
}

func (s *Server) unregisterSSE(id int64) {
	if id < 0 {
		return
	}
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	delete(s.sseCancels, id)
}

// trackForShutdown wraps a handler whose requests may be long-lived (the
// MCP streamable-HTTP SSE GET blocks on r.Context().Done()) so the shutdown
// sweep (cancelSSEStreams) can cancel them — mirroring /events/stream.
// Without this, http.Server.Shutdown waits the full drain budget for a
// connected /mcp client and then starves the job-queue drain (SPEC-02).
func (s *Server) trackForShutdown(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		id := s.registerSSE(cancel)
		defer func() {
			s.unregisterSSE(id)
			cancel()
		}()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// serveEventsStream is the bus-facing core of the SSE surface, shared by
// the single-workspace route and the multi-workspace registry path (which
// serves it WITHOUT a stack ref — the bus is registry-owned and outlives
// stacks, so a stream must not pin one).
func serveEventsStream(w http.ResponseWriter, r *http.Request, bus *events.Bus, register func(context.CancelFunc) int64, unregister func(int64)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		return
	}
	streamCtx, streamCancel := context.WithCancel(r.Context())
	defer streamCancel()
	if register != nil {
		id := register(streamCancel)
		defer unregister(id)
	}
	ch, unsub := bus.Subscribe(256)
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-streamCtx.Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				// Subscribe's contract for a closed bus: a closed channel,
				// never zero-value events in a tight loop.
				return
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// handleEventsStream is the SPEC-07 SSE surface: one `data:` line per
// event, a `: keepalive` comment every 15s, unsubscribe on disconnect.
// Token-gated like every non-health route (authMiddleware). Local UIs are
// the consumer; the stream is per-workspace by construction.
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Bus == nil {
		writeErr(w, http.StatusServiceUnavailable, "events_disabled", "event stream is not enabled for this workspace")
		return
	}
	serveEventsStream(w, r, s.cfg.Bus, s.registerSSE, s.unregisterSSE)
}
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ReadyFn != nil && s.cfg.ReadyFn() {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ready"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"status":"starting"}`))
}

// writeJSON emits v with the api envelope conventions (same content type
// as internal/api — F-063).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

// callTool executes one MCP tool and translates the result through the
// SHARED api translation (F-036 — one translator, two route tables).
func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (int, json.RawMessage, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res := s.mcp.CallTool(ctx, name, req)
	isErr, body := api.TranslateToolResult(name, res)
	if isErr {
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		msg := envelope.Error
		if msg == "" {
			msg = "tool error"
		}
		return http.StatusInternalServerError, nil, fmt.Errorf("%s", msg)
	}
	return http.StatusOK, body, nil
}

const maxJSONBody = 1 << 20 // 1 MiB, matching the /v1 dispatch cap

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var body struct {
		Content string `json:"content"`
		Context string `json:"context,omitempty"`
		Tags    string `json:"tags,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "capture requires {content}")
		return
	}
	status, raw, err := s.callTool(r.Context(), "wiki_capture", map[string]any{
		"content": body.Content, "context": body.Context, "tags": body.Tags,
	})
	if err != nil {
		writeErr(w, status, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var body struct {
		Query    string   `json:"query"`
		Limit    int      `json:"limit,omitempty"`
		Channels []string `json:"channels,omitempty"`
		Expand   bool     `json:"expand,omitempty"`
		Rerank   bool     `json:"rerank,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Query == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "search requires {query}")
		return
	}
	args := map[string]any{"query": body.Query}
	if body.Limit > 0 {
		args["limit"] = body.Limit
	}
	if len(body.Channels) > 0 {
		args["channels"] = strings.Join(body.Channels, ",")
	}
	if body.Expand {
		args["expand"] = true
	}
	if body.Rerank {
		args["rerank"] = true
	}
	status, raw, err := s.callTool(r.Context(), "wiki_search", args)
	if err != nil {
		writeErr(w, status, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handleCompile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	var req CompileJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "compile body must be JSON")
		return
	}
	j, err := s.queue.Submit(req)
	if err != nil {
		if errors.Is(err, ErrBacklog) {
			writeErr(w, http.StatusConflict, "conflict", err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": j.ID})
}

func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	j, ok := s.ledger.Get(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "job "+id+" not found")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleJobList(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.ledger.List(limit)})
}

func (s *Server) handleGraphQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "graph query requires ?q=")
		return
	}
	args := map[string]any{"question": q, "mode": r.URL.Query().Get("mode")}
	if asOf := r.URL.Query().Get("as_of"); asOf != "" {
		args["as_of"] = asOf
	}
	status, raw, err := s.callTool(r.Context(), "wiki_graph_query", args)
	if err != nil {
		writeErr(w, status, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handleDoc(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if strings.HasPrefix(path, "src:") {
		writeErr(w, http.StatusNotFound, "not_found", path+" is not an article id (only article DocIDs are served)")
		return
	}
	// Containment BEFORE the lookup: an encoded `..` bypasses ServeMux's
	// cleanPath and reaches the handler, so os.Stat on a workspace-escaped
	// path would be a host-filesystem existence oracle (N-03/SPEC-02 review).
	// Treat any escape as not-found — never Stat it.
	fullPath := filepath.Join(s.cfg.Workspace, path)
	if contained, err := pathsafe.Contained(s.cfg.Workspace, fullPath); err != nil || !contained {
		writeErr(w, http.StatusNotFound, "not_found", "article not found")
		return
	}
	// Classify not-found BEFORE the tool call (N-03): redacted errors are
	// path-free by design, so existence is decided here, not from text.
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		writeErr(w, http.StatusNotFound, "not_found", "article not found")
		return
	}
	_, raw, err := s.callTool(r.Context(), "wiki_read", map[string]any{"path": path})
	if err != nil {
		code := "internal"
		httpStatus := http.StatusInternalServerError
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			code = "not_found"
			httpStatus = http.StatusNotFound
		}
		writeErr(w, httpStatus, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", `attachment; filename="workspace.tar"`)
	if err := exportTar(r.Context(), s.cfg.Workspace, w); err != nil {
		// Headers may already be written; log-only at this point.
		return
	}
}

// exportTar streams a tar of dir — thin wrapper over the shared
// deterministic exporter (SPEC-04 D5). The live SQLite DB is copied as-is —
// a concurrent compile may leave it inconsistent (documented caveat;
// backup-API snapshot is out of scope).
func exportTar(ctx context.Context, dir string, dst io.Writer) error {
	return export.Tar(ctx, dir, dst)
}
