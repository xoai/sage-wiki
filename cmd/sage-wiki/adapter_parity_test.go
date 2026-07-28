package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/pflag"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/web"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// fakeEmbedVec is the deterministic "embedding" of a text: a tiny bag-of-words
// vector over a fixed vocabulary. Both the fake embedding endpoint and the
// fixture vectors use it, so vector ranking is reproducible without a network.
func fakeEmbedVec(text string) []float32 {
	vocab := []string{"vector", "graph", "chunk", "trust", "wiki", "search", "rank", "index"}
	v := make([]float32, len(vocab))
	lower := strings.ToLower(text)
	for i, w := range vocab {
		v[i] = float32(strings.Count(lower, w))
	}
	v[len(v)-1] += 0.1 // never the zero vector — cosine would be undefined
	return v
}

// fakeEmbedServer serves the OpenAI embeddings shape.
func fakeEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		resp := map[string]any{"data": []map[string]any{{"embedding": fakeEmbedVec(req.Input)}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type parityDoc struct {
	id      string
	content string
	tags    []string
	path    string
}

var parityCorpus = []parityDoc{
	{"concept:vector-search", "Vector search ranks a wiki index by embedding similarity.", []string{"search", "core"}, "wiki/concepts/vector-search.md"},
	{"concept:graph-rank", "Graph rank walks the ontology so a wiki search reaches neighbors.", []string{"search"}, "wiki/concepts/graph-rank.md"},
	{"concept:chunk-index", "Chunk index splits an article so search can rank passages.", []string{"indexing", "core"}, "wiki/concepts/chunk-index.md"},
	{"concept:trust-model", "Trust decides which wiki answers a search may return.", []string{"trust"}, "wiki/concepts/trust-model.md"},
	{"output:answered-search", "A generated answer about wiki search rank and trust.", []string{"output"}, "wiki/outputs/answered-search.md"},
}

// writeParityConfig pins every knob the adapters read, so the golden and the
// three entry points cannot disagree by reading different defaults.
func writeParityConfig(t *testing.T, dir, embedURL, includeOutputs string) *config.Config {
	return writePipelineConfig(t, dir, embedURL, includeOutputs, "unified")
}

func writePipelineConfig(t *testing.T, dir, embedURL, includeOutputs, pipeline string) *config.Config {
	t.Helper()
	cfg := fmt.Sprintf(`version: 1
project: parity
output: wiki
sources:
  - path: notes
    type: md
api:
  provider: openai
  api_key: test-key
models:
  summarize: m
  extract: m
  write: m
  lint: m
  query: m
embed:
  provider: openai
  model: fake-embed
  dimensions: 8
  api_key: test-key
  base_url: %s
search:
  hybrid_weight_bm25: 0.7
  hybrid_weight_vector: 0.3
  hybrid_weight_graph: 0.2
  default_limit: 10
  pipeline: %s
trust:
  include_outputs: %s
`, embedURL, pipeline, includeOutputs)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load pinned config: %v", err)
	}
	return loaded
}

// setupParityProject builds the fixed corpus: FTS entries, doc vectors from the
// same fake embedder the adapters will use, and a confirmed trust record for
// the one `output:` doc.
func setupParityProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "parity", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	for _, d := range parityCorpus {
		if err := memStore.Add(memory.Entry{ID: d.id, Content: d.content, Tags: d.tags, ArticlePath: d.path}); err != nil {
			t.Fatal(err)
		}
		if err := vecStore.Upsert(d.id, fakeEmbedVec(d.content)); err != nil {
			t.Fatal(err)
		}
		abs := filepath.Join(dir, d.path)
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(d.content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Dated docs (ADR-039): V-M3c's entry-point half — every surface must
	// emit the date it ranks with.
	for i, d := range parityCorpus {
		if err := memStore.SetSourceDate(d.id, int64(1700000000+i*86400)); err != nil {
			t.Fatal(err)
		}
	}

	// The output doc is confirmed, so include_outputs: verified admits it
	// while include_outputs: false still does not.
	ts := trust.NewStore(db)
	if err := ts.InsertPending(&store.PendingOutput{
		ID:       "answered-search",
		Question: "what is wiki search",
		Answer:   parityCorpus[len(parityCorpus)-1].content,
		State:    store.StatePending,
		FilePath: parityCorpus[len(parityCorpus)-1].path,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ts.SetState("answered-search", store.StateConfirmed); err != nil {
		t.Fatal(err)
	}

	return dir
}

// goldenDocs is the facade contract: what search.Run returns for a request the
// adapters are all supposed to build the same way.
func goldenDocs(t *testing.T, dir string, cfg *config.Config, query string, limit int, tags []string) []search.DocResult {
	t.Helper()
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	trustMode := cfg.Trust.IncludeOutputsMode()
	var ts *trust.Store
	if trustMode == "verified" {
		ts = trust.NewStore(db)
	}
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	resp, err := search.Run(context.Background(), search.Deps{
		Mem:                  memory.NewStore(db),
		Chunks:               memory.NewChunkStore(db),
		Vec:                  vectors.NewStore(db, vectors.WithANN(cfg.Search.ANNEnabled())),
		Embedder:             embed.NewFromConfig(cfg),
		BM25Weight:           cfg.Search.HybridWeightBM25,
		VectorWeight:         cfg.Search.HybridWeightVector,
		Ont:                  ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes)),
		GraphWeight:          cfg.Search.HybridWeightGraph,
		GraphRelationWeights: cfg.Search.GraphRelationWeights,
		IncludeDoc:           trust.IncludePredicate(trustMode, ts),
	}, search.Request{
		Query:             query,
		Limit:             limit,
		FilterTags:        tags,
		Granularity:       search.Docs,
		RerankMinCoverage: cfg.Search.RerankMinCoverageOrDefault(),
	})
	if err != nil {
		t.Fatalf("search.Run: %v", err)
	}
	return search.DocResults(resp.Results)
}

func ids(docs []search.DocResult) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.ID
	}
	return out
}

// row is the comparable projection of a result: everything an adapter could
// drop or reshape without changing the ID order (V-M5a compares results, not
// just identities).
type row struct {
	ID          string
	ArticlePath string
	Tags        string
	Content     string
	SourceDate  int64
	BM25Rank    int
	VectorRank  int
	GraphRank   int
	FinalScore  string
}

func rows(docs []search.DocResult) []row {
	out := make([]row, len(docs))
	for i, d := range docs {
		out[i] = row{
			ID: d.ID, ArticlePath: d.ArticlePath, Tags: strings.Join(d.Tags, "|"),
			Content: d.Content, SourceDate: d.SourceDate,
			BM25Rank: d.BM25Rank, VectorRank: d.VectorRank, GraphRank: d.GraphRank,
			FinalScore: fmt.Sprintf("%.6f", d.FinalScore),
		}
	}
	return out
}

func cliDocs(t *testing.T, dir, query string, limit int, tags []string) []search.DocResult {
	t.Helper()
	oldDir, oldFormat := projectDir, outputFormat
	projectDir, outputFormat = dir, "json"
	defer func() { projectDir, outputFormat = oldDir, oldFormat }()

	if err := searchCmd.Flags().Set("limit", fmt.Sprint(limit)); err != nil {
		t.Fatal(err)
	}
	// pflag APPENDS on a second Set of a slice flag, so per-case values must
	// be Replace'd — a Set("") would leave the previous case's tags in place.
	setTags := func(v []string) {
		sv, ok := searchCmd.Flags().Lookup("tags").Value.(pflag.SliceValue)
		if !ok {
			t.Fatal("tags flag is not a SliceValue")
		}
		if err := sv.Replace(v); err != nil {
			t.Fatal(err)
		}
	}
	setTags(tags)
	defer func() {
		_ = searchCmd.Flags().Set("limit", "10")
		setTags(nil)
	}()

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	err := runSearch(searchCmd, strings.Fields(query))
	w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	out, _ := io.ReadAll(r)

	var payload struct {
		Ok   bool               `json:"ok"`
		Data []search.DocResult `json:"data"`
		Err  json.RawMessage    `json:"error"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("decode CLI output %q: %v", out, err)
	}
	if !payload.Ok {
		t.Fatalf("CLI reported failure: %s", out)
	}
	return payload.Data
}

func mcpDocs(t *testing.T, dir, query string, limit int, tags []string) []search.DocResult {
	t.Helper()
	srv, err := wikimcp.NewServer(dir)
	if err != nil {
		t.Fatalf("mcp.NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	args := map[string]any{"query": query, "limit": float64(limit)}
	if len(tags) > 0 {
		args["tags"] = strings.Join(tags, ",")
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Arguments = args

	res := srv.CallTool(context.Background(), "wiki_search", req)
	if res.IsError {
		t.Fatalf("mcp wiki_search error: %+v", res.Content)
	}
	text, ok := mcpgo.AsTextContent(res.Content[0])
	if !ok {
		t.Fatalf("mcp result is not text: %+v", res.Content[0])
	}
	var payload struct {
		Results []search.DocResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(text.Text), &payload); err != nil {
		t.Fatalf("decode MCP output %q: %v", text.Text, err)
	}
	return payload.Results
}

// webHit is what the web surface exposes — fewer fields than DocResult, so
// parity there is asserted on the fields it does emit.
type webHit struct {
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Score      float64 `json:"score"`
	SourceDate int64   `json:"source_date"`
}

func webHits(t *testing.T, dir, query string, limit int, tags []string) []webHit {
	t.Helper()
	srv, err := web.NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	url := fmt.Sprintf("/api/search?q=%s&limit=%d", strings.ReplaceAll(query, " ", "+"), limit)
	if len(tags) > 0 {
		url += "&tags=" + strings.Join(tags, ",")
	}
	rec := httptest.NewRecorder()
	// Loopback host — the security middleware refuses httptest's default.
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "http://127.0.0.1"+url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("web /api/search: %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Results []webHit `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode web output %q: %v", rec.Body.String(), err)
	}
	return payload.Results
}

// V-M5a: every entry point returns exactly what search.Run returns, in the
// same order, for the same fixed corpus and query set — including the
// tag-filter and trust cases where a wiring slip is invisible on a plain query.
func TestAdapterParityWithSearchRun(t *testing.T) {
	dir := setupParityProject(t)
	embedSrv := fakeEmbedServer(t)

	cases := []struct {
		name           string
		query          string
		limit          int
		tags           []string
		includeOutputs string
		wantAbsent     []string
		wantPresent    []string
	}{
		{
			name: "plain query", query: "wiki search rank", limit: 10,
			includeOutputs: "false",
			wantAbsent:     []string{"output:answered-search"}, // trust default hides outputs
		},
		{
			name: "hard tag filter", query: "wiki search rank", limit: 10,
			tags: []string{"core"}, includeOutputs: "false",
			// `tags` is a hard AND filter, NOT a soft boost: a doc that
			// matches the query but lacks the tag must be absent entirely.
			wantAbsent:  []string{"concept:graph-rank", "concept:trust-model"},
			wantPresent: []string{"concept:vector-search", "concept:chunk-index"},
		},
		{
			name: "limit below corpus size", query: "wiki search rank", limit: 2,
			includeOutputs: "false",
		},
		{
			name: "trust include_outputs true", query: "wiki search rank trust", limit: 10,
			includeOutputs: "true",
			wantPresent:    []string{"output:answered-search"},
		},
		{
			name: "trust include_outputs verified", query: "wiki search rank trust", limit: 10,
			includeOutputs: "verified",
			wantPresent:    []string{"output:answered-search"}, // the fixture output is confirmed
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeParityConfig(t, dir, embedSrv.URL, tc.includeOutputs)
			want := goldenDocs(t, dir, cfg, tc.query, tc.limit, tc.tags)
			wantIDs := ids(want)
			if len(wantIDs) == 0 {
				t.Fatal("golden returned no results — fixture drift")
			}
			if tc.limit < len(parityCorpus) && len(wantIDs) > tc.limit {
				t.Fatalf("golden returned %d results, want <= limit %d", len(wantIDs), tc.limit)
			}
			for _, absent := range tc.wantAbsent {
				for _, got := range wantIDs {
					if got == absent {
						t.Fatalf("golden contains %s, want it excluded (%v)", absent, wantIDs)
					}
				}
			}
			for _, present := range tc.wantPresent {
				found := false
				for _, got := range wantIDs {
					if got == present {
						found = true
					}
				}
				if !found {
					t.Fatalf("golden missing %s (%v)", present, wantIDs)
				}
			}

			wantRows := rows(want)
			if wantRows[0].SourceDate == 0 {
				t.Fatal("golden carries no source date — V-M3c cannot be asserted")
			}

			for _, adapter := range []struct {
				name string
				fn   func(*testing.T, string, string, int, []string) []search.DocResult
			}{
				{"cli", cliDocs},
				{"mcp", mcpDocs},
			} {
				got := rows(adapter.fn(t, dir, tc.query, tc.limit, tc.tags))
				if fmt.Sprint(got) != fmt.Sprint(wantRows) {
					t.Errorf("%s rows differ from search.Run:\n got %+v\nwant %+v", adapter.name, got, wantRows)
				}
			}

			// Web emits a reduced shape; assert every field it does emit,
			// including the source date and the output-dir-stripped path.
			gotWeb := webHits(t, dir, tc.query, tc.limit, tc.tags)
			if len(gotWeb) != len(want) {
				t.Fatalf("web returned %d results, want %d", len(gotWeb), len(want))
			}
			for i, h := range gotWeb {
				w := want[i]
				if h.ID != w.ID {
					t.Errorf("web[%d].id = %s, want %s", i, h.ID, w.ID)
				}
				if wantPath := strings.TrimPrefix(w.ArticlePath, "wiki/"); h.Path != wantPath {
					t.Errorf("web[%d].path = %s, want %s", i, h.Path, wantPath)
				}
				if h.SourceDate != w.SourceDate {
					t.Errorf("web[%d].source_date = %d, want %d", i, h.SourceDate, w.SourceDate)
				}
				if fmt.Sprintf("%.6f", h.Score) != fmt.Sprintf("%.6f", w.FinalScore) {
					t.Errorf("web[%d].score = %f, want %f", i, h.Score, w.FinalScore)
				}
			}
		})
	}
}

// T5.1's rollback switch has to work in BOTH directions: `pipeline: legacy`
// must actually take every surface off the unified path, and the surfaces must
// still agree with each other there. A switch that only ever selects the new
// path is not a rollback.
func TestLegacyPipelinePinServesEverySurface(t *testing.T) {
	dir := setupParityProject(t)
	embedSrv := fakeEmbedServer(t)
	const query = "wiki search rank"

	cfg := writePipelineConfig(t, dir, embedSrv.URL, "true", "legacy")
	if cfg.Search.PipelineOrDefault() != "legacy" {
		t.Fatalf("PipelineOrDefault = %q, want legacy", cfg.Search.PipelineOrDefault())
	}

	cli := ids(cliDocs(t, dir, query, 10, nil))
	mcp := ids(mcpDocs(t, dir, query, 10, nil))
	var web []string
	for _, h := range webHits(t, dir, query, 10, nil) {
		web = append(web, h.ID)
	}
	if len(cli) == 0 {
		t.Fatal("legacy pin returned no CLI results")
	}
	if strings.Join(mcp, ",") != strings.Join(cli, ",") {
		t.Errorf("legacy MCP = %v, want CLI %v", mcp, cli)
	}
	if strings.Join(web, ",") != strings.Join(cli, ",") {
		t.Errorf("legacy web = %v, want CLI %v", web, cli)
	}

	// And the pin must actually be the legacy path, not unified-by-accident:
	// legacy is doc-level only, so it carries none of the unified additions.
	for _, d := range cliDocs(t, dir, query, 10, nil) {
		if d.FinalScore != 0 || d.GraphRank != 0 || d.SourceDate != 0 {
			t.Errorf("legacy result carries unified-only fields: %+v", d)
			break
		}
	}
}

// The soft tag boost was dead for the whole initiative — `Request.Tags` had no
// production caller, so spec §2.2's "+3%/tag, cap 15%" never ran outside
// tests. `--boost-tags` / `boost_tags` are its callers. Unlike `--tags`, a
// boost must change SCORES without changing membership.
func TestBoostTagsPromotesWithoutExcluding(t *testing.T) {
	dir := setupParityProject(t)
	embedSrv := fakeEmbedServer(t)
	writeParityConfig(t, dir, embedSrv.URL, "false")

	const query = "wiki search rank"

	run := func(t *testing.T, boost []string) []search.DocResult {
		t.Helper()
		oldDir, oldFormat := projectDir, outputFormat
		projectDir, outputFormat = dir, "json"
		defer func() { projectDir, outputFormat = oldDir, oldFormat }()

		sv, ok := searchCmd.Flags().Lookup("boost-tags").Value.(pflag.SliceValue)
		if !ok {
			t.Fatal("boost-tags is not a SliceValue")
		}
		if err := sv.Replace(boost); err != nil {
			t.Fatal(err)
		}
		defer sv.Replace(nil)

		r, w, _ := os.Pipe()
		oldStdout := os.Stdout
		os.Stdout = w
		err := runSearch(searchCmd, strings.Fields(query))
		w.Close()
		os.Stdout = oldStdout
		if err != nil {
			t.Fatalf("runSearch(boost=%v): %v", boost, err)
		}
		out, _ := io.ReadAll(r)
		var payload struct {
			Data []search.DocResult `json:"data"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		return payload.Data
	}

	plain := run(t, nil)
	boosted := run(t, []string{"indexing"})
	if len(plain) < 3 {
		t.Fatalf("fixture drift: %d results", len(plain))
	}

	// Membership is untouched: a soft boost is not a filter.
	if strings.Join(ids(plain), ",") != strings.Join(ids(boosted), ",") ||
		len(plain) != len(boosted) {
		// Order MAY differ; membership may not.
		got, want := append([]string{}, ids(boosted)...), append([]string{}, ids(plain)...)
		sort.Strings(got)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("boost changed membership: %v -> %v", want, got)
		}
	}

	scoreOf := func(rows []search.DocResult, id string) float64 {
		for _, r := range rows {
			if r.ID == id {
				return r.FinalScore
			}
		}
		t.Fatalf("%s absent from results", id)
		return 0
	}
	// concept:chunk-index carries `indexing`; concept:graph-rank does not.
	const tagged, untagged = "concept:chunk-index", "concept:graph-rank"
	gain := scoreOf(boosted, tagged) - scoreOf(plain, tagged)
	if gain < 0.029 || gain > 0.031 {
		t.Errorf("tagged doc gained %.4f, want the documented +0.03 for one matching tag", gain)
	}
	if d := scoreOf(boosted, untagged) - scoreOf(plain, untagged); d != 0 {
		t.Errorf("untagged doc moved by %.4f, want 0", d)
	}
}
