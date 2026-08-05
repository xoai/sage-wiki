package web

import (
	"context"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/fsnotify/fsnotify"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
)

// WebServer serves the web UI and REST API.
type WebServer struct {
	projectDir   string
	db           store.DBHandle
	backend      store.Backend
	mem          store.EntryStore
	vec          store.VectorStore
	ont          store.OntologyStore
	searcher     *hybrid.Searcher
	embedder     embed.Embedder
	cfg          *config.Config
	wsClients    map[chan string]bool
	wsMu         sync.Mutex
	queryRunning atomic.Int32 // concurrent query limiter

	// pollInterval is the output-watch polling fallback interval (default
	// 3s; tests inject a short interval). Zero means default.
	pollInterval time.Duration

	// token, when non-empty, gates /api/* and /ws (Bearer header or ?token=).
	// allowedHosts are extra Host values accepted beyond loopback (anti
	// DNS-rebind). Both default from config and are overridden by the serve
	// command from flags/env via SetAuth.
	token        string
	allowedHosts []string
	httpSrv      *http.Server // set in Serve; used for graceful shutdown

	// progress is the shared compile-progress hub (P2-3); nil when the
	// server runs without the worker (compile progress SSE then 503s).
	progress *compiler.Progress

	// v1Handler is the public /v1 REST facade (P4-1), mounted by Handler()
	// when set via SetV1Handler. It dispatches to the MCP tool handlers;
	// the web server adds only the existing security middleware around it.
	v1Handler http.Handler
}

// NewWebServer creates a web server sharing the project's stores.
// Construction is via the shared app container (P1-8); all fields are
// aliased from it, and s.db = a.DB so Close semantics are unchanged
// (closeOnce-idempotent).
func NewWebServer(projectDir string, progress ...*compiler.Progress) (*WebServer, error) {
	a, err := app.Open(projectDir)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}

	s := &WebServer{
		projectDir: projectDir,
		db:         a.DB,
		backend:    a.Backend,
		mem:        a.Mem,
		vec:        a.Vec,
		ont:        a.Ont,
		searcher:   a.Searcher,
		embedder:   a.Embedder(),
		cfg:        a.Config,
		wsClients:  make(map[chan string]bool),
	}
	if len(progress) > 0 {
		s.progress = progress[0]
	}
	s.SetAuth(a.Config.Serve.Token, splitHosts(a.Config.Serve.AllowedHost))
	return s, nil
}

// SetAuth sets the bearer token (empty disables auth) and the extra allowed
// Host values (beyond loopback). The serve command calls this to apply
// flag/env overrides on top of the config defaults.
func (s *WebServer) SetAuth(token string, allowedHosts []string) {
	s.token = token
	s.allowedHosts = allowedHosts
}

// SetV1Handler mounts the /v1 REST facade (P4-1). Called by the serve
// command after constructing the MCP-backed api.Router; Handler() mounts
// it inside the same security middleware as /api/*.
func (s *WebServer) SetV1Handler(h http.Handler) {
	s.v1Handler = h
}

// Config returns the server's loaded configuration (read-only callers —
// e.g. the /v1 facade's feature-gate pre-checks). It is the same *Config
// the server itself uses, so facade and server cannot diverge.
func (s *WebServer) Config() *config.Config {
	return s.cfg
}

// splitHosts parses a comma-separated host list, trimming blanks.
func splitHosts(csv string) []string {
	var out []string
	for _, h := range strings.Split(csv, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// Handler returns the HTTP handler with all routes registered.
func (s *WebServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// REST API
	mux.HandleFunc("/api/tree", s.handleTree)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/articles/", s.handleArticle)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/graph", s.handleGraph)
	mux.HandleFunc("/api/files/", s.handleFile)
	mux.HandleFunc("/api/query", s.handleQuery)
	mux.HandleFunc("/api/compile/status", s.handleCompileStatus)
	mux.HandleFunc("/api/compile/progress", s.handleCompileProgress)
	mux.HandleFunc("/api/provenance", s.handleProvenance)
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Public REST facade (P4-1) — same middleware envelope as /api/*.
	if s.v1Handler != nil {
		mux.Handle("/v1/", s.v1Handler)
	} else {
		// Without a facade wired, /v1/* must not fall through to the SPA
		// fallback and answer 200 HTML — answer the envelope instead.
		mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			api.WriteError(w, http.StatusNotFound, api.CodeNotFound, "not found", nil)
		}))
	}

	// Optional observability endpoint (P2-2). NOT build-tagged — ships in
	// the default (no-webui) binary. MCP SSE deliberately does not register
	// this (design D8: the MCP transport is not an ops surface).
	if s.cfg.Serve.Metrics {
		mux.Handle("/metrics", metrics.Handler())
	}

	// Static files + SPA fallback
	handler := defaultStaticHandler(s.projectDir)
	if staticHandler != nil {
		handler = staticHandler(s.projectDir)
	}
	mux.HandleFunc("/", handler)

	// Wrap with security middleware
	return s.securityMiddleware(mux)
}

// Serve builds the HTTP server with hardened timeouts, starts it, and shuts it
// down gracefully when ctx is cancelled (SIGINT/SIGTERM from the caller). It
// blocks until the server stops or errors.
func (s *WebServer) Serve(ctx context.Context, addr string) error {
	defer metrics.LogSnapshot() // P2-2 shutdown snapshot
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is deliberately 0: /api/query streams via SSE and would be
		// cut mid-stream by a global write deadline. SSE responses are bounded by
		// the request context and the per-query limiter instead.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	// Start output directory watcher for hot reload; it stops when ctx cancels.
	go s.watchOutputDir(ctx)

	log.Info("web UI starting", "addr", addr)
	fmt.Fprintf(os.Stderr, "\n🌐 sage-wiki web UI: http://%s\n\n", addr)

	errCh := make(chan error, 1)
	go func() {
		err := s.httpSrv.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("web UI shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("web: graceful shutdown: %w", err)
		}
		return <-errCh
	}
}

// Start serves without external lifecycle control (background context). Retained
// for callers that don't need graceful shutdown.
func (s *WebServer) Start(addr string) error {
	return s.Serve(context.Background(), addr)
}

// Close cleans up resources.
func (s *WebServer) Close() error {
	return s.backend.Close()
}

// watchOutputDir watches the output directory for changes and broadcasts
// reload until ctx is cancelled (server shutdown), so it does not outlive
// the server. Event-driven (fsnotify) where the platform supports it, with
// the pre-existing dirSnapshot poll as fallback (PERF-03, P1-5).
func (s *WebServer) watchOutputDir(ctx context.Context) {
	outputDir := filepath.Join(s.projectDir, s.cfg.Output)
	s.watchFsnotify(ctx, outputDir)
}

// watchFsnotify runs the event-driven watch, falling back to watchPoll
// when fsnotify can't work here: NewWatcher fails, the recursive add fails
// (e.g. the output dir doesn't exist yet), or the path is a /mnt/ WSL
// mount after symlink resolution — the same static check the compiler's
// watch mode uses (there is NO silent-no-events detection anywhere in the
// codebase; network drives not under /mnt/ silently fail in the compiler
// too, and this fallback inherits that gap).
func (s *WebServer) watchFsnotify(ctx context.Context, outputDir string) {
	resolved, err := filepath.EvalSymlinks(outputDir)
	if err == nil {
		outputDir = resolved
	}
	if strings.HasPrefix(outputDir, "/mnt/") {
		log.Info("web watch: /mnt/ path — using polling fallback", "path", outputDir)
		s.watchPoll(ctx, outputDir)
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Warn("web watch: fsnotify unavailable, using polling fallback", "error", err)
		s.watchPoll(ctx, outputDir)
		return
	}
	defer watcher.Close()

	if err := addRecursiveWatch(watcher, outputDir); err != nil {
		log.Warn("web watch: cannot watch output dir, using polling fallback", "path", outputDir, "error", err)
		s.watchPoll(ctx, outputDir)
		return
	}

	const debounce = 300 * time.Millisecond
	var timer *time.Timer
	var fired <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				log.Warn("web watch: fsnotify events channel closed — falling back to polling")
				s.watchPoll(ctx, outputDir)
				return
			}
			// Newly created subdirectories are added as they appear so
			// their files are watched too.
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					if err := watcher.Add(ev.Name); err != nil {
						// Watch-descriptor exhaustion (e.g. inotify
						// max_user_watches) hits at RUNTIME as the vault
						// grows — degrade to polling rather than silently
						// watching a partial tree.
						log.Warn("web watch: add subdir failed, falling back to polling", "path", ev.Name, "error", err)
						s.watchPoll(ctx, outputDir)
						return
					}
				}
			}
			// Only content ops debounce-broadcast; metadata-only events
			// (chmod) are ignored, matching the old size+mtime poll's view.
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(debounce)
			fired = timer.C
		case err, ok := <-watcher.Errors:
			if !ok {
				// Events AND Errors close together on internal watcher
				// failure — both paths must fall back, or the watch is
				// silently dead half the time (Gate-8 recheck).
				log.Warn("web watch: fsnotify errors channel closed — falling back to polling")
				s.watchPoll(ctx, outputDir)
				return
			}
			log.Warn("web watch: fsnotify error", "error", err)
		case <-fired:
			log.Info("wiki files changed, broadcasting reload")
			s.BroadcastReload()
			fired = nil
		}
	}
}

// addRecursiveWatch adds a directory and all subdirectories to the watcher.
// The root must exist — otherwise fsnotify would silently watch nothing and
// the caller's polling fallback would never engage (that gap is exactly
// what the fallback exists for). Duplicated from compiler's unexported
// addRecursive (~15 lines) rather than exporting API for one consumer.
func addRecursiveWatch(watcher *fsnotify.Watcher, dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("web watch: output dir: %w", err)
	}
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if err := watcher.Add(path); err != nil {
				return err
			}
		}
		return nil
	})
}

// watchPoll is the pre-P1-5 polling watcher: snapshot-compare on an
// interval (pollInterval, injectable for tests).
func (s *WebServer) watchPoll(ctx context.Context, outputDir string) {
	snapshot := s.dirSnapshot(outputDir)

	interval := s.pollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := s.dirSnapshot(outputDir)
			if current != snapshot {
				snapshot = current
				log.Info("wiki files changed, broadcasting reload")
				s.BroadcastReload()
			}
		}
	}
}

// dirSnapshot returns a quick hash of the output directory state.
func (s *WebServer) dirSnapshot(dir string) string {
	var total int64
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size() + info.ModTime().UnixNano()
		return nil
	})
	return fmt.Sprintf("%d", total)
}

// contentSecurityPolicy locks the SPA to same-origin scripts/styles/connections
// and forbids framing. 'unsafe-inline' is kept only for styles (Tailwind emits
// inline styles); scripts stay 'self'-only. data: is allowed for images.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; " +
	"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// securityMiddleware enforces the Host allowlist (anti DNS-rebind), bearer auth
// on /api/* and /ws when a token is configured, an Origin check on
// state-changing / streaming endpoints, and sets security headers.
func (s *WebServer) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// DNS-rebinding defense: the Host must be loopback or explicitly allowed.
		// Checked first so a rebound name reaches no handler at all.
		if !s.hostAllowed(r.Host) {
			if strings.HasPrefix(r.URL.Path, "/v1/") {
				api.WriteError(w, http.StatusForbidden, api.CodeForbidden, "host not allowed", nil)
			} else {
				http.Error(w, "host not allowed", http.StatusForbidden)
			}
			return
		}

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)

		// Auth gates /api/* and /ws when a token is configured; the static SPA
		// shell stays unauthenticated (it holds no data and must load to prompt
		// for / carry the token). /v1/* answers with the facade's envelope;
		// /api/* keeps its plain-text shape (byte-unchanged).
		if s.token != "" && requiresAuth(r.URL.Path) && !s.tokenValid(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			if strings.HasPrefix(r.URL.Path, "/v1/") {
				api.WriteError(w, http.StatusUnauthorized, api.CodeUnauthenticated, "missing or invalid bearer token", nil)
			} else {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}
			return
		}

		// Origin check for state-changing and streaming endpoints — defeats a
		// malicious page issuing cross-origin requests. /api/* keeps its
		// original POST-only scope (byte-unchanged); /v1/* extends it to every
		// state-changing method the facade exposes (POST/PUT/DELETE/PATCH).
		stateChanging := r.Method == http.MethodPost ||
			(strings.HasPrefix(r.URL.Path, "/v1/") &&
				(r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch))
		if stateChanging || r.URL.Path == "/api/query" {
			if origin := r.Header.Get("Origin"); origin != "" {
				if parsed, err := url.Parse(origin); err != nil || parsed.Host != r.Host {
					if strings.HasPrefix(r.URL.Path, "/v1/") {
						api.WriteError(w, http.StatusForbidden, api.CodeForbidden, "origin mismatch", nil)
					} else {
						http.Error(w, "origin mismatch", http.StatusForbidden)
					}
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
}

// requiresAuth reports whether a path is gated by the bearer token.
// /metrics is gated exactly like /api/* (P2-2: operational data).
// /v1/* is the public REST facade (P4-1) — gated identically.
func requiresAuth(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/v1/") || path == "/ws" || path == "/metrics"
}

// hostAllowed reports whether the request Host is loopback or explicitly allowed
// via SetAuth. An empty Host (HTTP/1.0, direct socket) is treated as loopback.
// Matching is case-insensitive (hostnames are case-insensitive). This is a
// DNS-rebinding defense for browsers; non-browser clients control their own Host,
// so the bearer token is the real gate for exposed binds.
func (s *WebServer) hostAllowed(host string) bool {
	h := strings.ToLower(hostOnly(host))
	switch h {
	case "", "127.0.0.1", "::1", "localhost":
		return true
	}
	for _, allowed := range s.allowedHosts {
		if h == strings.ToLower(hostOnly(allowed)) {
			return true
		}
	}
	return false
}

// hostOnly strips the port (and IPv6 brackets) from a Host header value.
func hostOnly(host string) string {
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// tokenValid reports whether the request presents the configured token via an
// Authorization: Bearer header or a ?token= query param. The query fallback
// exists because browser WebSockets cannot set headers and the SPA bootstraps
// its token from the URL. Compared in constant time.
func (s *WebServer) tokenValid(r *http.Request) bool {
	presented := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		presented = strings.TrimPrefix(auth, "Bearer ")
	} else {
		presented = r.URL.Query().Get("token")
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1
}

// Token returns the currently configured token (empty if auth is disabled).
func (s *WebServer) Token() string { return s.token }

// AllowedHosts returns the extra allowed Host values (beyond loopback).
func (s *WebServer) AllowedHosts() []string { return s.allowedHosts }

// SplitHosts parses a comma-separated host list, trimming blanks. Exported for
// the serve command to parse the --allowed-host flag / env.
func SplitHosts(csv string) []string { return splitHosts(csv) }

// IsLoopbackBind reports whether a --bind value is a loopback address. An empty
// bind is NOT loopback: net/http treats ":port" as every interface, so it must
// be authenticated like any other public bind.
func IsLoopbackBind(bind string) bool {
	switch bind {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// CheckBindAuth returns an error when binding to a non-loopback address without
// a token, which would expose the server unauthenticated. Loopback binds need
// no token (zero-config local use — see the serve command).
func CheckBindAuth(bind, token string) error {
	if token != "" || IsLoopbackBind(bind) {
		return nil
	}
	return fmt.Errorf("web: refusing to bind %q without a token: set --token or SAGE_WIKI_TOKEN (loopback binds need none)", bind)
}

// --- REST API Handlers ---

func (s *WebServer) handleTree(w http.ResponseWriter, r *http.Request) {
	outputDir := filepath.Join(s.projectDir, s.cfg.Output)

	type fileEntry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}

	tree := map[string]any{}

	// Scan each subdirectory
	for _, sub := range []string{"concepts", "summaries", "outputs"} {
		dir := filepath.Join(outputDir, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var files []fileEntry
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			files = append(files, fileEntry{
				Name: strings.TrimSuffix(e.Name(), ".md"),
				Path: filepath.Join(sub, e.Name()),
			})
		}
		tree[sub] = files
	}

	// Stats
	conceptCount := 0
	if c, ok := tree["concepts"].([]fileEntry); ok {
		conceptCount = len(c)
	}
	summaryCount := 0
	if s, ok := tree["summaries"].([]fileEntry); ok {
		summaryCount = len(s)
	}

	tree["stats"] = map[string]int{
		"concepts":  conceptCount,
		"summaries": summaryCount,
	}

	writeJSON(w, tree)
}

func (s *WebServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	entryCount, _ := s.mem.Count()
	vecCount, _ := s.vec.Count()
	vecDims, _ := s.vec.Dimensions()
	entityCount, _ := s.ont.EntityCount("")
	relCount, _ := s.ont.RelationCount()

	writeJSON(w, map[string]any{
		"project":    s.cfg.Project,
		"entries":    entryCount,
		"vectors":    vecCount,
		"dimensions": vecDims,
		"entities":   entityCount,
		"relations":  relCount,
	})
}

func (s *WebServer) handleArticle(w http.ResponseWriter, r *http.Request) {
	// Extract path from URL: /api/articles/concepts/self-attention.md
	articlePath := strings.TrimPrefix(r.URL.Path, "/api/articles/")
	if articlePath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Ensure .md extension
	if !strings.HasSuffix(articlePath, ".md") {
		articlePath += ".md"
	}

	// Serve only from the output dir. pathsafe rejects traversal, symlink
	// escapes, and sibling-prefix dirs (e.g. <output>-secret) that a bare
	// strings.HasPrefix against the project root would wrongly allow.
	absPath, err := pathsafe.SafeJoin(filepath.Join(s.projectDir, s.cfg.Output), articlePath)
	if err != nil {
		http.Error(w, "path traversal not allowed", http.StatusForbidden)
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, "article not found", http.StatusNotFound)
		return
	}

	content := string(data)

	// Parse frontmatter
	var frontmatter map[string]any
	body := content
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---")
		if end >= 0 {
			fmText := content[4 : 4+end]
			body = strings.TrimSpace(content[4+end+4:])
			frontmatter = parseFrontmatterSimple(fmText)
		}
	}

	writeJSON(w, map[string]any{
		"path":        articlePath,
		"frontmatter": frontmatter,
		"body":        body,
	})
}

func (s *WebServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, map[string]any{"results": []any{}, "total": 0})
		return
	}

	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	var tags []string
	if t := r.URL.Query().Get("tags"); t != "" {
		tags = strings.Split(t, ",")
	}

	type webResult struct {
		id, content, articlePath string
		score                    float64
		sourceDate               int64
	}
	var results []webResult

	// The trust rule applies on both branches — `search.pipeline: legacy`
	// rolls back ranking, never the rule about which documents may appear.
	trustMode := s.cfg.Trust.IncludeOutputsMode()
	var trustStore *trust.Store
	if trustMode == "verified" {
		trustStore = trust.NewStore(s.db)
	}
	includeDoc := trust.IncludePredicate(trustMode, trustStore)

	if s.cfg.Search.PipelineOrDefault() == "unified" {
		// Unified pipeline (ADR-036, M5). LLM stages stay off on the
		// web surface.
		var chunkStore store.ChunkStore
		if s.backend != nil {
			chunkStore = s.backend.Chunks()
		} else {
			chunkStore = memory.NewChunkStore(s.db)
		}
		resp, err := search.Run(r.Context(), search.Deps{
			Mem:                  s.mem,
			Chunks:               chunkStore,
			Vec:                  s.vec,
			Embedder:             s.embedder,
			BM25Weight:           s.cfg.Search.HybridWeightBM25,
			VectorWeight:         s.cfg.Search.HybridWeightVector,
			Ont:                  s.ont,
			GraphWeight:          s.cfg.Search.HybridWeightGraph,
			GraphRelationWeights: s.cfg.Search.GraphRelationWeights,
			IncludeDoc:           includeDoc,
		}, search.Request{
			Query:       query,
			Limit:       limit,
			FilterTags:  tags,
			Granularity: search.Docs,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, r := range resp.Results {
			results = append(results, webResult{id: r.DocID, content: r.ChunkText,
				articlePath: r.ArticlePath, score: r.FinalScore, sourceDate: r.SourceDate})
		}
	} else {
		// Legacy doc-level path (config pin).
		var queryVec []float32
		if s.embedder != nil {
			v, embedErr := s.embedder.Embed(query)
			if embedErr != nil {
				log.Warn("web search embed failed, falling back to BM25-only", "error", embedErr)
			} else {
				queryVec = v
			}
		}
		legacy, err := s.searcher.Search(hybrid.SearchOpts{
			Query:        query,
			Tags:         tags,
			Limit:        limit,
			BM25Weight:   s.cfg.Search.HybridWeightBM25,
			VectorWeight: s.cfg.Search.HybridWeightVector,
		}, queryVec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, r := range legacy {
			if !includeDoc(r.ID) {
				continue
			}
			results = append(results, webResult{id: r.ID, content: r.Content,
				articlePath: r.ArticlePath, score: r.RRFScore})
		}
	}

	type searchHit struct {
		ID         string  `json:"id"`
		Path       string  `json:"path"`
		Snippet    string  `json:"snippet"`
		Score      float64 `json:"score"`
		SourceDate int64   `json:"source_date,omitempty"`
	}

	var hits []searchHit
	outputPrefix := s.cfg.Output + "/"
	for _, r := range results {
		snippet := r.content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		// Strip output dir prefix so paths are relative (e.g. "summaries/foo.md" not "_wiki/summaries/foo.md")
		articlePath := strings.TrimPrefix(r.articlePath, outputPrefix)
		hits = append(hits, searchHit{
			ID:         r.id,
			Path:       articlePath,
			Snippet:    snippet,
			SourceDate: r.sourceDate,
			Score:      r.score,
		})
	}

	writeJSON(w, map[string]any{
		"query":   query,
		"results": hits,
		"total":   len(hits),
	})
}

func (s *WebServer) handleGraph(w http.ResponseWriter, r *http.Request) {
	center := r.URL.Query().Get("center")
	depth := 2

	if d := r.URL.Query().Get("depth"); d != "" {
		fmt.Sscanf(d, "%d", &depth)
	}

	type node struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		Connections int    `json:"connections"`
	}
	type edge struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		Relation string `json:"relation"`
	}

	var nodes []node
	var edges []edge

	if center != "" {
		// The requested center may be an alias. Resolve ONCE and let every
		// use — the traversal, the node set, the center-entity lookup, and
		// the response's center field — see the same canonical id; resolving
		// per-use would let the four drift apart.
		center = store.CanonicalOrSelf(s.ont, center)

		// Neighborhood query
		entities, _ := s.ont.Traverse(center, ontology.TraverseOpts{
			Direction: ontology.Both,
			MaxDepth:  depth,
		})

		nodeSet := map[string]bool{center: true}
		for _, e := range entities {
			nodeSet[e.ID] = true
		}

		// Get center entity
		if ce, _ := s.ont.GetEntity(center); ce != nil {
			rels, _ := s.ont.GetRelations(ce.ID, ontology.Both, "")
			nodes = append(nodes, node{ID: ce.ID, Type: ce.Type, Name: ce.Name, Connections: len(rels)})
		}

		for _, e := range entities {
			rels, _ := s.ont.GetRelations(e.ID, ontology.Both, "")
			nodes = append(nodes, node{ID: e.ID, Type: e.Type, Name: e.Name, Connections: len(rels)})

			for _, rel := range rels {
				if nodeSet[rel.SourceID] && nodeSet[rel.TargetID] {
					edges = append(edges, edge{Source: rel.SourceID, Target: rel.TargetID, Relation: rel.Relation})
				}
			}
		}
	} else {
		// Full graph — exclude source entities (noise in overview)
		allEntities, _ := s.ont.ListEntities("")

		// Pre-compute connection counts in a single query (avoids N+1)
		connCounts, err := s.ont.EntityConnectionCounts()
		if err != nil {
			connCounts = map[string]int{}
		}

		entitySet := make(map[string]bool)
		for _, e := range allEntities {
			if e.Type == "source" {
				continue // skip source nodes from overview graph
			}
			entitySet[e.ID] = true
			nodes = append(nodes, node{ID: e.ID, Type: e.Type, Name: e.Name, Connections: connCounts[e.ID]})
		}

		// All relations (only between non-source entities)
		if rels, err := s.ont.AllRelations(); err == nil {
			for _, rel := range rels {
				if entitySet[rel.SourceID] && entitySet[rel.TargetID] {
					edges = append(edges, edge{Source: rel.SourceID, Target: rel.TargetID, Relation: rel.Relation})
				}
			}
		}
	}

	resp := map[string]any{
		"nodes": nodes,
		"edges": edges,
		"total": len(nodes),
	}
	if center != "" {
		// Additive: tells the frontend which node the neighborhood is
		// actually centered on — after alias resolution it may differ from
		// the ?center= parameter the client sent.
		resp["center"] = center
	}
	writeJSON(w, resp)
}

func (s *WebServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Rate limit: max 1 concurrent, reject at 2+
	current := s.queryRunning.Add(1)
	if current > 1 {
		s.queryRunning.Add(-1)
		http.Error(w, "query already in progress, try again shortly", http.StatusTooManyRequests)
		return
	}
	defer s.queryRunning.Add(-1)

	var body struct {
		Question string `json:"question"`
		TopK     int    `json:"top_k"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64KB max
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Question == "" {
		http.Error(w, "question required", http.StatusBadRequest)
		return
	}
	// SPEC-08 D1: overlong questions fail fast before any provider call.
	lim := s.cfg.Limits.Resolve()
	if int64(len(body.Question)) > lim.MaxQueryBytes {
		http.Error(w, fmt.Sprintf("question too large: %v", limits.New(limits.WhichQueryBytes, lim.MaxQueryBytes, int64(len(body.Question)))), http.StatusRequestEntityTooLarge)
		return
	}
	if body.TopK <= 0 {
		body.TopK = 5
	}

	// Set up SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Stream query with token callback; use request context for cancellation
	sources, err := query.StreamQuery(r.Context(), s.projectDir, body.Question, body.TopK, func(token string) {
		fmt.Fprintf(w, "event: token\ndata: %s\n\n", mustJSON(map[string]string{"text": token}))
		flusher.Flush()
	}, s.db)

	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", mustJSON(map[string]string{"error": err.Error()}))
		flusher.Flush()
		return
	}

	// Send sources
	fmt.Fprintf(w, "event: sources\ndata: %s\n\n", mustJSON(map[string]any{"paths": sources}))
	flusher.Flush()

	// Done
	fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

// handleCompileStatus returns a JSON snapshot of the durable compile queue
// (P2-3, spec C6): counts by status plus the active lease holder.
func (s *WebServer) handleCompileStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.backend.CompileItems().Stats()
	if err != nil {
		http.Error(w, "compile stats unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"pending":        stats.ByStatus["pending"],
		"leased":         stats.ByStatus["leased"],
		"done":           stats.ByStatus["done"],
		"failed":         stats.ByStatus["failed"],
		"active_owner":   stats.ActiveOwner,
		"last_heartbeat": stats.LastHeartbeat,
	})
}

// handleCompileProgress streams compile events as SSE (P2-3, spec C6),
// following the /api/query pattern: flusher, no global write deadline,
// unsubscribe when the client disconnects so slow/dead clients never
// accumulate (drop-on-full protects the worker meanwhile).
func (s *WebServer) handleCompileProgress(w http.ResponseWriter, r *http.Request) {
	if s.progress == nil {
		http.Error(w, "compile progress unavailable (no worker)", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Flush headers immediately — otherwise the client blocks on response
	// headers until the first event, and an idle queue deadlocks the stream.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	events, unsub := s.progress.Subscribe(64)
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, mustJSON(ev))
			flusher.Flush()
		}
	}
}

// handleWebSocket upgrades to WebSocket for hot reload notifications.
func (s *WebServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Minimal WebSocket upgrade (RFC 6455)
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "websocket required", http.StatusBadRequest)
		return
	}

	// Reject cross-origin upgrades before hijacking. Host and bearer auth are
	// already enforced by securityMiddleware; Origin defends against a malicious
	// page opening a socket to a loopback server the user is authed to.
	if origin := r.Header.Get("Origin"); origin != "" {
		if parsed, err := url.Parse(origin); err != nil || parsed.Host != r.Host {
			http.Error(w, "origin mismatch", http.StatusForbidden)
			return
		}
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	// Compute accept key
	const magicGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New()
	h.Write([]byte(key + magicGUID))
	acceptKey := base64.StdEncoding.EncodeToString(h.Sum(nil))

	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Send upgrade response
	buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	buf.WriteString("Upgrade: websocket\r\n")
	buf.WriteString("Connection: Upgrade\r\n")
	buf.WriteString("Sec-WebSocket-Accept: " + acceptKey + "\r\n\r\n")
	buf.Flush()

	// Register client
	ch := make(chan string, 4)
	s.wsMu.Lock()
	s.wsClients[ch] = true
	s.wsMu.Unlock()

	defer conn.Close()

	// Send messages to client
	go func() {
		for msg := range ch {
			wsWriteText(conn, msg)
		}
	}()

	// Read loop (just to detect close)
	readBuf := make([]byte, 256)
	for {
		_, err := conn.Read(readBuf)
		if err != nil {
			// Remove from map BEFORE closing channel to prevent BroadcastReload
			// from sending on a closed channel (race condition → panic).
			s.wsMu.Lock()
			delete(s.wsClients, ch)
			s.wsMu.Unlock()
			close(ch)
			return
		}
	}
}

// wsWriteText sends a WebSocket text frame.
func wsWriteText(conn net.Conn, msg string) {
	data := []byte(msg)
	frame := []byte{0x81} // FIN + text opcode
	if len(data) < 126 {
		frame = append(frame, byte(len(data)))
	} else {
		frame = append(frame, 126, byte(len(data)>>8), byte(len(data)&0xFF))
	}
	frame = append(frame, data...)
	conn.Write(frame)
}

// BroadcastReload notifies all WebSocket clients to reload.
func (s *WebServer) BroadcastReload() {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for ch := range s.wsClients {
		select {
		case ch <- "reload":
		default: // skip slow clients
		}
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (s *WebServer) handleFile(w http.ResponseWriter, r *http.Request) {
	// Serve files (images, etc.) from the output directory.
	// /api/files/concepts/image.png → <output>/concepts/image.png
	filePath := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if filePath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	// Serve only from the output dir (same containment as handleArticle).
	absPath, err := pathsafe.SafeJoin(filepath.Join(s.projectDir, s.cfg.Output), filePath)
	if err != nil {
		http.Error(w, "path traversal not allowed", http.StatusForbidden)
		return
	}

	// Only serve known safe file types
	ext := strings.ToLower(filepath.Ext(filePath))
	allowed := map[string]string{
		".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".gif": "image/gif", ".svg": "image/svg+xml", ".webp": "image/webp",
		".pdf": "application/pdf",
	}
	contentType, ok := allowed[ext]
	if !ok {
		http.Error(w, "file type not allowed", http.StatusForbidden)
		return
	}

	// SVG can embed inline scripts; neutralize it — force download and a locked
	// CSP so it cannot execute even if opened directly. Other image types stay
	// inline for the SPA.
	if ext == ".svg" {
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	}

	f, err := os.Open(absPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// ServeContent adds Range support (206) and conditional caching while keeping
	// our explicit Content-Type and the extension allowlist above.
	http.ServeContent(w, r, filepath.Base(absPath), info.ModTime(), f)
}

// staticHandler is overridden by the webui build tag to serve embedded assets.
var staticHandler func(projectDir string) http.HandlerFunc

// defaultStaticHandler serves a fallback page when web UI is not built.
func defaultStaticHandler(projectDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/wiki/") || r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!DOCTYPE html><html><body>
<h1>sage-wiki</h1>
<p>Web UI not built. Build with: <code>go build -tags webui</code></p>
<p>API available at <a href="/api/status">/api/status</a></p>
</body></html>`)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *WebServer) handleProvenance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	source := r.URL.Query().Get("source")
	article := r.URL.Query().Get("article")

	if source == "" && article == "" {
		http.Error(w, "either 'source' or 'article' query parameter required", http.StatusBadRequest)
		return
	}

	mfPath := filepath.Join(s.projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		http.Error(w, "failed to load manifest", http.StatusInternalServerError)
		return
	}
	mf.SetNow(config.NowUTC)

	if source != "" {
		articles := mf.ArticlesFromSource(source)
		items := make([]map[string]string, 0, len(articles))
		for _, name := range articles {
			c := mf.Concepts[name]
			items = append(items, map[string]string{"concept": name, "article_path": c.ArticlePath})
		}
		writeJSON(w, map[string]any{"source": source, "articles": items, "total": len(items)})
		return
	}

	sources := mf.SourcesForArticle(article)
	writeJSON(w, map[string]any{"article": article, "sources": sources, "total": len(sources)})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func parseFrontmatterSimple(fm string) map[string]any {
	result := map[string]any{}
	for _, line := range strings.Split(fm, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			result[key] = val
		}
	}
	return result
}
