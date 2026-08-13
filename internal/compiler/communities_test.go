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
	"github.com/xoai/sage-wiki/internal/manifest"
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
			// Each triangle has exactly 3 intra-community edges (gates i3:
			// the per-level memberSets fix would ship silently without this).
			if c.EdgeCount != 3 {
				t.Errorf("community %s EdgeCount = %d, want 3", c.ID, c.EdgeCount)
			}
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
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
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

// Gates i2: the empty-graph path must clear stale communities (not leave
// GlobalQA answering from a graph that no longer exists).
func TestCommunitiesPassEmptyGraphClears(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)
	if len(f.communities(t)) == 0 {
		t.Fatal("seed run produced no communities")
	}

	// Delete every relation → detection input is empty → clear expected.
	rels, err := f.ont.AllRelations()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if err := f.ont.DeleteEntity(r.SourceID); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t)
	if got := f.communities(t); len(got) != 0 {
		t.Errorf("empty graph must clear all communities, got %+v", got)
	}
	// Orphan files swept too.
	entries, err := os.ReadDir(filepath.Join(f.dir, "wiki", "communities"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("orphan community file survived: %s", e.Name())
	}
}

// Gates i2: a file whose DB row is gone (crash window) is swept even though
// no ReplaceDetection will ever return its ID.
func TestSweepCommunityFilesOrphan(t *testing.T) {
	f := newCommunityFixture(t)
	dir := filepath.Join(f.dir, "wiki", "communities")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "c0-0.md"), []byte("kept"), 0o644)
	os.WriteFile(filepath.Join(dir, "c0-9.md"), []byte("orphan"), 0o644)
	sweepCommunityFiles(filepath.Join(f.dir, "wiki"), map[string]bool{"c0-0": true})
	if _, err := os.Stat(filepath.Join(dir, "c0-0.md")); err != nil {
		t.Error("kept file was swept")
	}
	if _, err := os.Stat(filepath.Join(dir, "c0-9.md")); !os.IsNotExist(err) {
		t.Error("orphan file survived the sweep")
	}
}

// T7 (Gate-8 Critical): batch-resume wiring — a resumed batch compile with
// communities enabled runs the pass at its tail.
func TestResumeBatchWiresCommunitiesPass(t *testing.T) {
	fake := newFakeBatchServer(t)
	fake.status.Store("completed")
	dir := writeBatchProject(t, fake.URL, `
ontology:
  communities:
    enabled: true
`, "raw/a.md", "raw/b.md")

	idA, idB := batchIDForPath("raw/a.md"), batchIDForPath("raw/b.md")
	if err := saveBatchCheckpoint(dir, &BatchCheckpoint{
		CompileID: "c1",
		Batch: &BatchState{
			BatchID:  "batch_test_1",
			Provider: "openai",
			Pass:     "summarize",
			PathByID: map[string]string{idA: "raw/a.md", idB: "raw/b.md"},
		},
		Pending: []string{"raw/a.md", "raw/b.md"},
	}); err != nil {
		t.Fatal(err)
	}
	fake.setResults([]string{idA, idB})

	// Pre-seed the detectable graph.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	ont := ontology.NewStore(db, nil, nil)
	seedGraph(t, ont)
	db.Close()

	if _, err := Compile(dir, CompileOpts{}); err != nil {
		t.Fatalf("resume compile: %v", err)
	}
	assertCommunitiesExist(t, dir)
}

// T7 (Gate-8 Critical): ReExtract wiring — re-extract with communities
// enabled runs the pass at its tail.
func TestReExtractWiresCommunitiesPass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"content": `[{"name": "test-concept", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`,
			}}},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	defer srv.Close()

	dir := writeBatchProject(t, srv.URL, `
ontology:
  communities:
    enabled: true
`, "raw/a.md")

	// ReExtract needs existing summaries on disk.
	if err := os.MkdirAll(filepath.Join(dir, "wiki", "summaries"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "wiki", "summaries", "a.md"), []byte(
		"---\nsource: raw/a.md\n---\n\n## Key claims\n\nSelf-attention computes contextual representations of tokens across the sequence and relates to test-concept.\n"), 0o644)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	ont := ontology.NewStore(db, nil, nil)
	seedGraph(t, ont)
	db.Close()

	if _, err := ReExtract(dir); err != nil {
		t.Fatalf("ReExtract: %v", err)
	}
	assertCommunitiesExist(t, dir)
}

func seedGraph(t *testing.T, ont *ontology.Store) {
	t.Helper()
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
}

func assertCommunitiesExist(t *testing.T, dir string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ont := ontology.NewStore(db, nil, nil)
	comms, err := ont.ListCommunities(-1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comms) == 0 {
		t.Error("no community rows — pass not wired on this path")
	}
}

// Gate-8: model change invalidates cached summaries (regen with zero
// membership change).
func TestCommunitiesPassModelChangeInvalidates(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)
	before := f.calls.Load()
	if before == 0 {
		t.Fatal("seed run made no LLM calls")
	}

	f.cfg.Models.Extract = "other-model"
	f.run(t)
	if got := f.calls.Load() - before; got == 0 {
		t.Error("model change must regenerate summaries even with unchanged membership")
	}
}

// Gate-8: LLM failure on one community leaves its hash stale (retried next
// run) and does not fail the pass or other communities.
func TestCommunitiesPassToleratesLLMFailure(t *testing.T) {
	f := newCommunityFixture(t)
	f.srv.Close() // kill the LLM endpoint entirely
	f.seed(t)
	f.run(t) // must not panic or fail

	comms := f.communities(t)
	if len(comms) == 0 {
		t.Fatal("no communities — test is vacuous without rows")
	}
	for _, c := range comms {
		if c.Summary != "" {
			t.Errorf("community %s summarized despite dead LLM", c.ID)
		}
		if c.SummaryHash != "" {
			t.Errorf("community %s has a stale-false hash after failure", c.ID)
		}
	}
}

// Gate-8: min_members raise → artifacts cleared; later lower → re-summarized.
func TestCommunitiesPassMinMembersRoundTrip(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)
	before := f.calls.Load()

	f.cfg.Ontology.Communities.MinMembers = 100
	f.run(t)
	if len(f.communities(t)) == 0 {
		t.Fatal("raise deleted rows outright — expected clear-in-place")
	}
	for _, c := range f.communities(t) {
		if c.Summary != "" || c.SummaryHash != "" {
			t.Errorf("raise must clear stored summaries: %+v", c)
		}
	}

	f.cfg.Ontology.Communities.MinMembers = 3
	f.run(t)
	if got := f.calls.Load() - before; got == 0 {
		t.Error("lowering min_members must re-summarize (b2 clear made hashes stale)")
	}
}

// Gate-8: community files never register in output_index/manifest — the
// reconcile orphan drop must not eat them (spec M2).
func TestCommunityFilesExcludedFromReconcile(t *testing.T) {
	f := newCommunityFixture(t)
	f.seed(t)
	f.run(t)

	m, err := manifest.Load(filepath.Join(f.dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("manifest must exist after a compile pass ran: %v", err)
	}
	if m != nil {
		for path := range m.Sources {
			if strings.Contains(path, "communities/") {
				t.Errorf("community file leaked into manifest sources: %s", path)
			}
		}
		for path := range m.Concepts {
			if strings.Contains(path, "communities/") {
				t.Errorf("community file leaked into manifest concepts: %s", path)
			}
		}
	}
}
