package web

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// WebServer serves the web UI and REST API.
type WebServer struct {
	projectDir string
	db         *storage.DB
	mem        *memory.Store
	vec        *vectors.Store
	ont        *ontology.Store
	searcher   *hybrid.Searcher
	cfg        *config.Config
	wsClients    map[chan string]bool
	wsMu         sync.Mutex
	queryRunning atomic.Int32 // concurrent query limiter
}

// NewWebServer creates a web server sharing the project's stores.
func NewWebServer(projectDir string) (*WebServer, error) {
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("web: load config: %w", err)
	}

	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("web: open db: %w", err)
	}

	mem := memory.NewStore(db)
	vec := vectors.NewStore(db)
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	ont := ontology.NewStore(db, ontology.ValidRelationNames(merged))
	searcher := hybrid.NewSearcher(mem, vec)

	return &WebServer{
		projectDir: projectDir,
		db:         db,
		mem:        mem,
		vec:        vec,
		ont:        ont,
		searcher:   searcher,
		cfg:        cfg,
		wsClients:  make(map[chan string]bool),
	}, nil
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
	mux.HandleFunc("/api/provenance", s.handleProvenance)
	mux.HandleFunc("/ws", s.handleWebSocket)

	// Static files + SPA fallback
	handler := defaultStaticHandler(s.projectDir)
	if staticHandler != nil {
		handler = staticHandler(s.projectDir)
	}
	mux.HandleFunc("/", handler)

	// Wrap with security middleware
	return s.securityMiddleware(mux)
}

// Start begins serving the web UI and watches the output directory for changes.
func (s *WebServer) Start(addr string) error {
	handler := s.Handler()

	// Start output directory watcher for hot reload
	go s.watchOutputDir()

	log.Info("web UI starting", "addr", addr)
	fmt.Fprintf(os.Stderr, "\n🌐 sage-wiki web UI: http://%s\n\n", addr)
	return http.ListenAndServe(addr, handler)
}

// Close cleans up resources.
func (s *WebServer) Close() error {
	return s.db.Close()
}

// watchOutputDir polls the output directory for changes and broadcasts reload.
func (s *WebServer) watchOutputDir() {
	outputDir := filepath.Join(s.projectDir, s.cfg.Output)
	snapshot := s.dirSnapshot(outputDir)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		current := s.dirSnapshot(outputDir)
		if current != snapshot {
			snapshot = current
			log.Info("wiki files changed, broadcasting reload")
			s.BroadcastReload()
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

// securityMiddleware adds CORS and Origin checking.
func (s *WebServer) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS: same-origin only
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		// CSRF check for mutation endpoints
		if r.Method == "POST" {
			origin := r.Header.Get("Origin")
			if origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host {
					http.Error(w, "CSRF: origin mismatch", http.StatusForbidden)
					return
				}
			}
		}

		next.ServeHTTP(w, r)
	})
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
		"project":   s.cfg.Project,
		"entries":   entryCount,
		"vectors":   vecCount,
		"dimensions": vecDims,
		"entities":  entityCount,
		"relations": relCount,
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

	absPath := filepath.Join(s.projectDir, s.cfg.Output, articlePath)

	// Path traversal protection
	absProject, _ := filepath.Abs(s.projectDir)
	absResolved, _ := filepath.Abs(absPath)
	if !strings.HasPrefix(absResolved, absProject) {
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

	results, err := s.searcher.Search(hybrid.SearchOpts{
		Query: query,
		Tags:  tags,
		Limit: limit,
	}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type searchHit struct {
		ID      string  `json:"id"`
		Path    string  `json:"path"`
		Snippet string  `json:"snippet"`
		Score   float64 `json:"score"`
	}

	var hits []searchHit
	outputPrefix := s.cfg.Output + "/"
	for _, r := range results {
		snippet := r.Content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		// Strip output dir prefix so paths are relative (e.g. "summaries/foo.md" not "_wiki/summaries/foo.md")
		articlePath := strings.TrimPrefix(r.ArticlePath, outputPrefix)
		hits = append(hits, searchHit{
			ID:      r.ID,
			Path:    articlePath,
			Snippet: snippet,
			Score:   r.RRFScore,
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
		Target  string `json:"target"`
		Relation string `json:"relation"`
	}

	var nodes []node
	var edges []edge

	if center != "" {
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
		connCounts := make(map[string]int)
		countRows, err := s.db.ReadDB().Query(`
			SELECT id, cnt FROM (
				SELECT source_id AS id, COUNT(*) AS cnt FROM relations GROUP BY source_id
				UNION ALL
				SELECT target_id AS id, COUNT(*) AS cnt FROM relations GROUP BY target_id
			) GROUP BY id`)
		if err == nil {
			defer countRows.Close()
			for countRows.Next() {
				var id string
				var cnt int
				countRows.Scan(&id, &cnt)
				connCounts[id] += cnt
			}
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
		rows, err := s.db.ReadDB().Query("SELECT source_id, target_id, relation FROM relations")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var e edge
				rows.Scan(&e.Source, &e.Target, &e.Relation)
				if entitySet[e.Source] && entitySet[e.Target] {
					edges = append(edges, e)
				}
			}
		}
	}

	writeJSON(w, map[string]any{
		"nodes": nodes,
		"edges": edges,
		"total": len(nodes),
	})
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

// handleWebSocket upgrades to WebSocket for hot reload notifications.
func (s *WebServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Minimal WebSocket upgrade (RFC 6455)
	if r.Header.Get("Upgrade") != "websocket" {
		http.Error(w, "websocket required", http.StatusBadRequest)
		return
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

	absPath := filepath.Join(s.projectDir, s.cfg.Output, filePath)

	// Path traversal protection
	absProject, _ := filepath.Abs(s.projectDir)
	absResolved, _ := filepath.Abs(absPath)
	if !strings.HasPrefix(absResolved, absProject) {
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

	data, err := os.ReadFile(absPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
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
