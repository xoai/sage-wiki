package compiler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// deferredStub is a deterministic provider whose write-pass responses carry
// per-concept delays, so goroutine COMPLETION order can be made to differ
// from input order. SPEC-04 D3: apply order must follow input order anyway.
type deferredStub struct {
	mu            sync.Mutex               // guards writeDelays/embedDelays swaps between runs
	writeDelays   map[string]time.Duration // substring of the concept name in the write prompt → delay
	embedDelays   map[string]time.Duration // substring of the chunk text in the embed input → delay
	requests      *syncCounter
	embeds        *syncCounter
	summarizeLog  *stringLog // source paths seen in summarize prompts (AC-3 scoping)
	writeLog      *stringLog // concept names seen in write prompts
	extractInputs *stringLog // source paths whose summaries reached the extract prompt
}

type stringLog struct {
	mu    sync.Mutex
	items []string
}

func (l *stringLog) add(s string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.items = append(l.items, s)
	l.mu.Unlock()
}

func (l *stringLog) clear() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.items = nil
	l.mu.Unlock()
}

func (l *stringLog) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}

type syncCounter struct {
	mu    chan struct{}
	count int
}

func (c *syncCounter) inc() { c.mu <- struct{}{}; c.count++; <-c.mu }
func (c *syncCounter) get() int {
	if c == nil {
		return 0
	}
	c.mu <- struct{}{}
	defer func() { <-c.mu }()
	return c.count
}

func (s *deferredStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)

		if body["input"] != nil {
			if s.embeds != nil {
				s.embeds.inc()
			}
			input, _ := body["input"].(string)
			s.mu.Lock()
			for sub, d := range s.embedDelays {
				if strings.Contains(input, sub) {
					time.Sleep(d)
				}
			}
			s.mu.Unlock()
			// Strongly distinct vectors per text (dominant dimension rotates
			// with FNV): near-identical embeddings would make dedup merge
			// distinct concepts (cosine ≥ 0.85).
			h := fnv.New32a()
			h.Write([]byte(input))
			vec := []float64{0.05, 0.05, 0.05, 0.05}
			vec[h.Sum32()%4] = 0.95
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": vec, "index": 0}},
			})
			return
		}
		s.requests.inc()

		messages, _ := body["messages"].([]any)
		var allText string
		for _, m := range messages {
			if mm, ok := m.(map[string]any); ok {
				if c, _ := mm["content"].(string); ok {
					allText += c
				}
			}
		}

		isExtract := strings.Contains(allText, "concept extraction system")
		isWrite := strings.Contains(allText, "wiki author writing a comprehensive article")
		if isExtract {
			for _, doc := range []string{"raw/doc1.md", "raw/doc2.md", "raw/doc3.md"} {
				if strings.Contains(allText, doc) {
					s.extractInputs.add(doc)
				}
			}
		}
		if isWrite {
			for _, n := range []string{"concept-aaa", "concept-bbb", "concept-ccc"} {
				if strings.Contains(allText, n) {
					s.writeLog.add(n)
				}
			}
		}
		if !isExtract && !isWrite {
			for _, doc := range []string{"raw/doc1.md", "raw/doc2.md", "raw/doc3.md"} {
				if strings.Contains(allText, doc) {
					s.summarizeLog.add(doc)
				}
			}
		}

		var content string
		switch {
		case isExtract:
			content = `[
			  {"name": "concept-aaa", "aliases": [], "sources": ["raw/doc1.md"], "type": "concept"},
			  {"name": "concept-bbb", "aliases": [], "sources": ["raw/doc2.md"], "type": "concept"},
			  {"name": "concept-ccc", "aliases": [], "sources": ["raw/doc3.md"], "type": "concept"}
			]`
		case isWrite:
			s.mu.Lock()
			for name, d := range s.writeDelays {
				if strings.Contains(allText, name) {
					time.Sleep(d)
				}
			}
			s.mu.Unlock()
			name := "concept-aaa"
			for _, n := range []string{"concept-aaa", "concept-bbb", "concept-ccc"} {
				if strings.Contains(allText, n) {
					name = n
				}
			}
			content = "# " + name + "\n\nDeferred-application test article body with enough content to pass validation checks.\n\n## See also\n\n[[concept-aaa]]"
		default:
			content = "## Key claims\n\nDeferred application test body with sufficient length to pass the summary quality gate.\n\n## Concepts\n\nconcept-aaa: A test concept."
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 60},
		})
	}
}

func compileDeferredCorpus(t *testing.T, serverURL string, ctx context.Context) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "defer", "gpt-4o-mini")
	cfg := fmt.Sprintf(`
version: 1
project: defer
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: %s
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 4
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`, serverURL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"doc1.md", "doc2.md", "doc3.md"} {
		body := fmt.Sprintf("# Deferred Doc %d\n\nDeferred application corpus content %d.", i+1, i+1)
		if err := os.WriteFile(filepath.Join(dir, "raw", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pinCorpusMtimes(t, dir)
	opts := CompileOpts{}
	if ctx != nil {
		opts.Ctx = ctx
	}
	Compile(dir, opts) // error inspected by callers via workspace state
	return dir
}

func entityRowidOrder(t *testing.T, dir string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT id FROM entities WHERE type = 'concept' ORDER BY rowid")
	if err != nil {
		t.Fatalf("entities query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("entity scan: %v", err)
	}
	return out
}

// TestDeferredWrite_AppliesInInputOrder pins SPEC-04 D3: even when goroutine
// completion order differs across runs, store mutations land in input order.
func TestDeferredWrite_AppliesInInputOrder(t *testing.T) {
	want := []string{"concept-aaa", "concept-bbb", "concept-ccc"}

	// Run A: bbb responds SLOWLY (completes last despite being second).
	stubA := &deferredStub{writeDelays: map[string]time.Duration{"concept-bbb": 200 * time.Millisecond}, requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srvA := httptest.NewServer(stubA.handler())
	dirA := compileDeferredCorpus(t, srvA.URL, nil)
	srvA.Close()

	// Run B: aaa responds SLOWLY (completes last despite being first).
	stubB := &deferredStub{writeDelays: map[string]time.Duration{"concept-aaa": 200 * time.Millisecond}, requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srvB := httptest.NewServer(stubB.handler())
	dirB := compileDeferredCorpus(t, srvB.URL, nil)
	srvB.Close()

	gotA := entityRowidOrder(t, dirA)
	gotB := entityRowidOrder(t, dirB)
	if strings.Join(gotA, ",") != strings.Join(want, ",") {
		t.Errorf("run A entity rowid order = %v, want %v (input order)", gotA, want)
	}
	if strings.Join(gotB, ",") != strings.Join(want, ",") {
		t.Errorf("run B entity rowid order = %v, want %v (input order)", gotB, want)
	}
}

// TestDeferredWrite_CancelDuringApply pins the P1-1 regression cell (spec
// test 6): a cancel observed mid-apply stops the apply; the run is
// incomplete, so pass flags are never marked — the next compile reprocesses.
func TestDeferredWrite_CancelDuringApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeApplyHookForTest = func(idx int) {
		if idx == 1 {
			cancel() // item 0 applied; cancel lands before item 1
		}
	}
	defer func() { writeApplyHookForTest = nil }()

	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := httptest.NewServer(stub.handler())
	dir := compileDeferredCorpus(t, srv.URL, ctx)
	srv.Close()

	if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", "concept-aaa.md")); err != nil {
		t.Errorf("concept-aaa article missing after partial apply: %v", err)
	}
	for _, name := range []string{"concept-bbb", "concept-ccc"} {
		if _, err := os.Stat(filepath.Join(dir, "wiki", "concepts", name+".md")); !os.IsNotExist(err) {
			t.Errorf("%s article present after cancel-before-its-apply (want absent)", name)
		}
	}

	// P1-1: an incomplete run marks nothing — pass_written stays 0 for all.
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var written int
	if err := db.QueryRow("SELECT COUNT(*) FROM compile_items WHERE pass_written = 1").Scan(&written); err != nil {
		t.Fatalf("count written: %v", err)
	}
	if written != 0 {
		t.Errorf("pass_written=1 rows = %d, want 0 (incomplete run marks nothing)", written)
	}
}

// chunkDocidRowidOrder returns chunk docids in rowid (insertion) order.
func chunkDocidRowidOrder(t *testing.T, dir string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT DISTINCT doc_id FROM chunks_meta WHERE doc_id LIKE 'src:%' ORDER BY rowid")
	if err != nil {
		t.Fatalf("chunks query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("chunk scan: %v", err)
	}
	return out
}

// TestDeferredEmbed_AppliesInInputOrder pins SPEC-04 D3 for the tier-1 embed
// pass: scrambled embed completion order must not change chunk rowid order.
func TestDeferredEmbed_AppliesInInputOrder(t *testing.T) {
	want := []string{"src:raw/doc1.md", "src:raw/doc2.md", "src:raw/doc3.md"}

	delayA := &deferredStub{embedDelays: map[string]time.Duration{"Deferred Doc 2": 250 * time.Millisecond}, requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srvA := httptest.NewServer(delayA.handler())
	dirA := compileDeferredCorpus(t, srvA.URL, nil)
	srvA.Close()

	delayB := &deferredStub{embedDelays: map[string]time.Duration{"Deferred Doc 1": 250 * time.Millisecond}, requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srvB := httptest.NewServer(delayB.handler())
	dirB := compileDeferredCorpus(t, srvB.URL, nil)
	srvB.Close()

	gotA := chunkDocidRowidOrder(t, dirA)
	gotB := chunkDocidRowidOrder(t, dirB)
	if strings.Join(gotA, ",") != strings.Join(want, ",") {
		t.Errorf("run A chunk rowid order = %v, want %v (input order)", gotA, want)
	}
	if strings.Join(gotB, ",") != strings.Join(want, ",") {
		t.Errorf("run B chunk rowid order = %v, want %v (input order)", gotB, want)
	}
}

// TestDeferredEmbed_CancelDuringApply pins the P1-1 regression cell for the
// embed pass (spec test 6): a cancel observed mid-apply stops the apply and
// leaves pass flags unmarked.
func TestDeferredEmbed_CancelDuringApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	indexApplyHookForTest = func(idx int) {
		if idx == 1 {
			cancel()
		}
	}
	defer func() { indexApplyHookForTest = nil }()

	stub := &deferredStub{requests: &syncCounter{mu: make(chan struct{}, 1)}, embeds: &syncCounter{mu: make(chan struct{}, 1)}}
	srv := httptest.NewServer(stub.handler())
	dir := compileDeferredCorpus(t, srv.URL, ctx)
	srv.Close()

	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var doc2Chunks int
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks_meta WHERE doc_id = 'src:raw/doc2.md'").Scan(&doc2Chunks); err != nil {
		t.Fatalf("count doc2 chunks: %v", err)
	}
	if doc2Chunks != 0 {
		t.Errorf("doc2 chunks = %d, want 0 (cancel before its apply)", doc2Chunks)
	}

	// Item 0 fully applied BEFORE the cancel → its per-item checkpoint is
	// legitimate (the tier system's sticky-flag resume design); items 1+ were
	// never applied and stay unmarked — the P1-1 shape for this pass.
	var embedded int
	if err := db.QueryRow("SELECT COUNT(*) FROM compile_items WHERE pass_embedded = 1").Scan(&embedded); err != nil {
		t.Fatalf("count embedded: %v", err)
	}
	if embedded != 1 {
		t.Errorf("pass_embedded=1 rows = %d, want 1 (only the item applied before the cancel)", embedded)
	}
}

// newTestServer starts the deferredStub's HTTP server.
func newTestServer(s *deferredStub) *httptest.Server {
	return httptest.NewServer(s.handler())
}

// compileDeferredCorpusOpts is compileDeferredCorpus with explicit CompileOpts.
func compileDeferredCorpusOpts(t *testing.T, serverURL string, opts CompileOpts) string {
	t.Helper()
	dir := t.TempDir()
	writeDeferredCorpusInto(t, dir, serverURL)
	if _, err := Compile(dir, opts); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return dir
}

// writeDeferredCorpusInto lays the 3-doc corpus + stub-pointed config into dir.
func writeDeferredCorpusInto(t *testing.T, dir, serverURL string) {
	t.Helper()
	wiki.InitGreenfield(dir, "defer", "gpt-4o-mini")
	cfg := fmt.Sprintf(`
version: 1
project: defer
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: %s
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 4
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`, serverURL)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"doc1.md", "doc2.md", "doc3.md"} {
		body := fmt.Sprintf("# Deferred Doc %d\n\nDeferred application corpus content %d.", i+1, i+1)
		if err := os.WriteFile(filepath.Join(dir, "raw", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pinCorpusMtimes(t, dir)
}

// compileInDir re-runs Compile in an existing deferred-corpus dir.
func compileInDir(t *testing.T, dir, serverURL string, opts CompileOpts) *CompileResult {
	t.Helper()
	res, err := Compile(dir, opts)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return res
}

// pinCorpusMtimes pins every raw/*.md to a fixed mtime: entry_dates source
// dates resolve from mtime (frontmatter > mtime > first-seen), and two
// harness runs write files seconds apart — unpinned, that input metadata
// (not compile nondeterminism) breaks byte-parity (parity's
// BuildWorkspaceAuth pins mtimes for the same reason).
func pinCorpusMtimes(t *testing.T, dir string) {
	t.Helper()
	fixed := time.Unix(1700000000, 0)
	entries, err := os.ReadDir(filepath.Join(dir, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(dir, "raw", e.Name()), fixed, fixed); err != nil {
			t.Fatal(err)
		}
	}
}
