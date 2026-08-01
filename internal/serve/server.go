package serve

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// Config is the server's construction surface (spec §2.2).
type Config struct {
	Workspace             string
	Tokens                []string
	MaxConcurrentCompiles int
	DrainTimeout          time.Duration
	RateLimit             func(next http.Handler) http.Handler
	Addr                  string
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
}

// New builds the server: job ledger + queue, routes, MCP mount.
func New(deps *Deps, mcpSrv *mcppkg.Server, cfg Config) (*Server, error) {
	if cfg.MaxConcurrentCompiles < 1 {
		cfg.MaxConcurrentCompiles = 2
	}
	if cfg.DrainTimeout < 10*time.Second {
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
	s.queue = NewQueue(ledger, cfg.MaxConcurrentCompiles, s.execCompile, nil)
	s.routes()
	s.mcpStream = s.mountMCP()
	return s, nil
}

// Queue exposes the job queue (the drain sequence stops it).
func (s *Server) Queue() *Queue { return s.queue }

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

// writeJSON emits v with the api envelope conventions.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

// callTool executes one MCP tool and re-emits its result: text that
// parses as JSON is passed through verbatim; other text wraps.
func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (int, json.RawMessage, error) {
	req := mcpgo.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res := s.mcp.CallTool(ctx, name, req)
	if res == nil {
		return http.StatusInternalServerError, nil, fmt.Errorf("tool %s returned no result", name)
	}
	if res.IsError {
		msg := "tool error"
		for _, c := range res.Content {
			if t, ok := c.(mcpgo.TextContent); ok {
				msg = t.Text
				break
			}
		}
		return http.StatusInternalServerError, nil, fmt.Errorf("%s", msg)
	}
	for _, c := range res.Content {
		if t, ok := c.(mcpgo.TextContent); ok {
			raw := json.RawMessage(t.Text)
			var probe any
			if json.Unmarshal(raw, &probe) == nil {
				return http.StatusOK, raw, nil
			}
			wrapped, _ := json.Marshal(map[string]string{"result": t.Text})
			return http.StatusOK, wrapped, nil
		}
	}
	return http.StatusOK, json.RawMessage(`{}`), nil
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
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
	var req CompileJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_argument", "compile body must be JSON")
		return
	}
	j, err := s.queue.Submit(req)
	if err != nil {
		writeErr(w, http.StatusConflict, "conflict", err.Error())
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
	status, raw, err := s.callTool(r.Context(), "wiki_read", map[string]any{"path": path})
	if err != nil {
		writeErr(w, status, "not_found", err.Error())
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

// exportTar streams a tar of dir, ctx-honoring (no engine — a small
// local helper per adaptation #1, F-024).
func exportTar(ctx context.Context, dir string, dst io.Writer) error {
	tw := tar.NewWriter(dst)
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err := tw.Close(); walkErr == nil {
		walkErr = err
	}
	return walkErr
}
