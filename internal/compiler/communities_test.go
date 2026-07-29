package compiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// P3-5 T6: CommunitiesPass — detection, persistence, summary caching,
// incremental regen, cleanup ordering.

type communityFixture struct {
	ont   *ontology.Store
	mem   *memory.Store
	vec   *vectors.Store
	dir   string
	cfg   *config.Config
	calls *atomic.Int64
	srv   *httptest.Server
}

func newCommunityFixture(t *testing.T) *communityFixture {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ont := ontology.NewStore(db, nil, nil)
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": "A theme about testing communities.\n\nKeywords: alpha, beta, gamma",
			}}},
			"model": "m",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Output: "wiki"}
	cfg.Ontology.Communities.Enabled = true
	cfg.Models.Extract = "m"
	return &communityFixture{
		ont:   ont,
		mem:   memory.NewStore(db),
		vec:   vectors.NewStore(db),
		dir:   dir,
		cfg:   cfg,
		calls: &calls,
		srv:   srv,
	}
}

// twoTriangles seeds {a,b,c} and {d,e,f} communities (3+ members each, so
// both are summary-eligible at the default min_members=3).
func (f *communityFixture) seed(t *testing.T) {
	t.Helper()
	for _, id := range []string{"a", "b", "c", "d", "e", "g"} {
		if err := f.ont.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	edges := [][2]string{
		{"a", "b"}, {"b", "c"}, {"a", "c"},
		{"d", "e"}, {"d", "g"}, {"e", "g"},
	}
	for i, e := range edges {
		if err := f.ont.AddRelation(store.Relation{
			ID: string(rune('A' + i)), SourceID: e[0], TargetID: e[1], Relation: "extends",
			Confidence: 0.9, Evidence: "link",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *communityFixture) run(t *testing.T) {
	t.Helper()
	CommunitiesPass(context.Background(), f.dir, f.ont, f.ont, f.mem, f.vec, nil, f.cfg,
		triplesClient(t, f.srv.URL))
}

func (f *communityFixture) communities(t *testing.T) []store.Community {
	t.Helper()
	comms, err := f.ont.ListCommunities(-1)
	if err != nil {
		t.Fatal(err)
	}
	return comms
}

func TestCommunitiesPassDetectsAndSummarizes(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)

	comms := f.communities(t)
	if len(comms) < 2 {
		t.Fatalf("want >= 2 communities, got %+v", comms)
	}
	summarized := 0
	for _, c := range comms {
		if c.Level == 0 && c.MemberCount >= 3 {
			if c.Summary == "" {
				t.Errorf("eligible community %s has no summary", c.ID)
				continue
			}
			summarized++
			// File + index written.
			if _, err := os.Stat(filepath.Join(f.dir, "wiki", "communities", c.ID+".md")); err != nil {
				t.Errorf("community file missing for %s: %v", c.ID, err)
			}
		}
	}
	if summarized != 2 {
		t.Errorf("summarized = %d, want 2", summarized)
	}
	if f.calls.Load() != 2 {
		t.Errorf("LLM calls = %d, want 2 (one per eligible community)", f.calls.Load())
	}
	// FTS index carries the community docs.
	results, err := f.mem.Search("testing communities", nil, 10)
	if err != nil || len(results) == 0 {
		t.Errorf("community summary not searchable: %v %v", results, err)
	}
}

func TestCommunitiesPassSecondRunNoOp(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)
	first := f.calls.Load()
	f.run(t) // unchanged graph → member hashes match → zero new LLM calls
	if got := f.calls.Load(); got != first {
		t.Errorf("unchanged graph re-run made %d new LLM calls", got-first)
	}
}

func TestCommunitiesPassRegeneratesOnlyChanged(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)
	before := f.calls.Load()

	// Add a new member to one triangle → only that community re-summarizes.
	if err := f.ont.AddEntity(store.Entity{ID: "h", Type: "concept", Name: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := f.ont.AddRelation(store.Relation{ID: "zz", SourceID: "a", TargetID: "h", Relation: "extends", Confidence: 0.9}); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if got := f.calls.Load() - before; got != 1 {
		t.Errorf("regen calls = %d, want exactly 1 (changed community only)", got)
	}
}

func TestCommunitiesPassBelowMinGetsNoSummary(t *testing.T) {
	f := newCommunityFixture(t)
	// One pair only: below min_members=3.
	for _, id := range []string{"x", "y"} {
		if err := f.ont.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.ont.AddRelation(store.Relation{ID: "r", SourceID: "x", TargetID: "y", Relation: "extends", Confidence: 0.9}); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	comms := f.communities(t)
	for _, c := range comms {
		if c.Summary != "" {
			t.Errorf("below-min community %s must not be summarized", c.ID)
		}
	}
	if f.calls.Load() != 0 {
		t.Errorf("below-min vault must make no LLM calls, got %d", f.calls.Load())
	}
}

func TestCommunitiesPassDisabledNoOp(t *testing.T) {
	f := newCommunityFixture(t)
	f.cfg.Ontology.Communities.Enabled = false
	f.seed(t)
	f.run(t)
	if len(f.communities(t)) != 0 {
		t.Error("disabled pass must write nothing")
	}
	if f.calls.Load() != 0 {
		t.Error("disabled pass must make no LLM calls")
	}
}

// T7: full-pipeline wiring — a compile with communities enabled ends with
// community rows persisted (the deferred pass ran on the final graph).
func TestCompileWiresCommunitiesPass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}
		var content string
		switch {
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name": "test-concept", "aliases": [], "sources": ["raw/article1.md"], "type": "concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			content = "---\nconcept: test-concept\n---\n\n# Test Concept\n\nA test concept."
		case strings.Contains(lastMsg, "theme of this knowledge-graph community"):
			content = "A theme about testing communities.\n\nKeywords: alpha, beta"
		default:
			content = "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject matter at sufficient length.\n\n## Concepts\n\ntest-concept: A fundamental concept."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
	cfgContent := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  default_tier: 3
ontology:
  communities:
    enabled: true
`
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0644)
	os.WriteFile(filepath.Join(dir, "raw", "article1.md"), []byte("# Self-Attention\n\nSelf-attention computes contextual representations."), 0644)

	// Pre-seed the graph the pass will detect (compile opens the same DB).
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	ont := ontology.NewStore(db, nil, nil)
	for _, id := range []string{"a", "b", "c", "d", "e", "g"} {
		if err := ont.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	for i, e := range [][2]string{{"a", "b"}, {"b", "c"}, {"a", "c"}, {"d", "e"}, {"d", "g"}, {"e", "g"}} {
		if err := ont.AddRelation(store.Relation{
			ID: string(rune('A' + i)), SourceID: e[0], TargetID: e[1], Relation: "extends", Confidence: 0.9,
		}); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	db2, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	ont2 := ontology.NewStore(db2, nil, nil)
	comms, err := ont2.ListCommunities(-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comms) == 0 {
		t.Error("compile with communities enabled produced no community rows — pass not wired")
	}
	summarized := 0
	for _, c := range comms {
		if c.Summary != "" {
			summarized++
		}
	}
	if summarized == 0 {
		t.Error("no community summaries generated during compile")
	}
}
