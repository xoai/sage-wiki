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
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/pkg/engine"
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
	httpSrv   *http.Server
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
	s := &Server{cfg: cfg, mcp: mcpSrv, deps: deps, ledger: ledger}
	s.queue = NewQueue(ledger, cfg.MaxConcurrentCompiles, semaphoreWrap(cfg.CompileSem, s.execCompile), nil)
	s.routes()
	s.mcpStream = s.mountMCP()
	// /v1 stays live on this listener too (Q-3): the existing facade over
	// the same MCP dispatch + the deps job runner.
	apiCfg, err := config.Load(filepath.Join(cfg.Workspace, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("serve: load config for /v1: %w", err)
	}
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
	s.httpSrv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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
	// Envelope 404 for unmatched paths (spec §2.2 error contract).
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not_found", r.Method+" "+r.URL.Path+": no such route")
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
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
	// Classify not-found BEFORE the tool call (N-03): redacted errors are
	// path-free by design, so existence is decided here, not from text.
	if _, err := os.Stat(filepath.Join(s.cfg.Workspace, path)); os.IsNotExist(err) {
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
