package search

import (
	"context"
	"reflect"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// graphFixture: entities self-attention (article) --cites--> transformer
// (article) --contradicts--> rnn (article); "flash" has NO article and must
// never surface. Entries exist for the three articles.
func graphFixture(t *testing.T) (Deps, store.OntologyStore) {
	t.Helper()
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)
	ont := ontology.NewStore(db, nil, nil)

	for _, e := range []ontology.Entity{
		{ID: "self-attention", Type: "concept", Name: "Self-Attention", ArticlePath: "wiki/concepts/self-attention.md"},
		{ID: "transformer", Type: "concept", Name: "Transformer", ArticlePath: "wiki/concepts/transformer.md"},
		{ID: "rnn", Type: "concept", Name: "RNN", ArticlePath: "wiki/concepts/rnn.md"},
		{ID: "flash", Type: "concept", Name: "Flash"}, // no article
	} {
		if err := ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []ontology.Relation{
		{ID: "r1", SourceID: "self-attention", TargetID: "transformer", Relation: "cites"},
		{ID: "r2", SourceID: "transformer", TargetID: "rnn", Relation: "contradicts"},
		{ID: "r3", SourceID: "self-attention", TargetID: "flash", Relation: "cites"},
	} {
		if err := ont.AddRelation(r); err != nil {
			t.Fatal(err)
		}
	}

	// Entry contents are lexically DISJOINT from each other so the
	// ablation half can prove graph-only reachability: only the
	// self-attention entry matches a "self attention" query.
	contents := map[string]string{
		"self-attention": "self attention mechanism overview",
		"transformer":    "transformer architecture deep dive",
		"rnn":            "recurrent network sequential processing",
	}
	for id, c := range contents {
		ms.Add(memory.Entry{ID: "concept:" + id, Content: c, ArticlePath: "wiki/concepts/" + id + ".md"})
	}

	return Deps{Mem: ms, Chunks: cs, Vec: vs, Ont: ont, GraphWeight: 0.5}, ont
}

// T4.1+T4.2: seeding finds the entity from query tokens (hyphenated bigram),
// BFS surfaces neighbors, articles only, ranked by distance/weight.
func TestGraphLegSeedsAndTraverses(t *testing.T) {
	deps, _ := graphFixture(t)

	leg, aliases := buildGraphLeg(deps.Ont, "self attention mechanisms", 10, nil)
	if len(aliases) != 0 {
		t.Errorf("no alias seeds expected, got %v", aliases)
	}
	ids := make([]string, len(leg.hits))
	for i, h := range leg.hits {
		ids[i] = h.docID
	}
	// Seed first (d=0), then d=1 neighbor, then d=2. "flash" (no article)
	// must be absent.
	want := []string{"concept:self-attention", "concept:transformer", "concept:rnn"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("leg order = %v, want %v", ids, want)
	}
}

// T4.2: a cites edge (0.7) ranks its target BELOW a same-distance
// contradicts edge (1.1) — rank score is distance/weight.
func TestGraphLegRelationWeights(t *testing.T) {
	db := openTestDB(t)
	ont := ontology.NewStore(db, nil, nil)
	for _, e := range []ontology.Entity{
		{ID: "hub", Type: "concept", Name: "Hub", ArticlePath: "wiki/concepts/hub.md"},
		{ID: "cited", Type: "concept", Name: "Cited", ArticlePath: "wiki/concepts/cited.md"},
		{ID: "contra", Type: "concept", Name: "Contra", ArticlePath: "wiki/concepts/contra.md"},
	} {
		ont.AddEntity(e)
	}
	ont.AddRelation(ontology.Relation{ID: "r1", SourceID: "hub", TargetID: "cited", Relation: "cites"})
	ont.AddRelation(ontology.Relation{ID: "r2", SourceID: "hub", TargetID: "contra", Relation: "contradicts"})

	leg, _ := buildGraphLeg(ont, "hub", 10, nil)
	ids := make([]string, len(leg.hits))
	for i, h := range leg.hits {
		ids[i] = h.docID
	}
	// d=1: contra score 1/1.1 ≈ 0.909 < cited 1/0.7 ≈ 1.428.
	want := []string{"concept:hub", "concept:contra", "concept:cited"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("weighted order = %v, want %v", ids, want)
	}
}

// T4.1: an alias seed resolves to the canonical and annotates alias_of.
// P3-3 semantics: an alias is itself an ENTITY merged into a canonical, so
// the alias entity must exist before LinkAlias records anything.
func TestGraphLegAliasSeed(t *testing.T) {
	deps, ont := graphFixture(t)
	os, ok := ont.(*ontology.Store)
	if !ok {
		t.Fatal("fixture ontology is not *ontology.Store")
	}
	if err := os.AddEntity(ontology.Entity{ID: "sdpa", Type: "concept", Name: "SDPA"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.LinkAlias(store.EntityAlias{Alias: "sdpa", CanonicalID: "self-attention", Status: store.AliasApplied, DecidedBy: "test"}); err != nil {
		t.Fatalf("link alias: %v", err)
	}

	leg, aliases := buildGraphLeg(deps.Ont, "sdpa internals", 10, nil)
	if len(leg.hits) == 0 || leg.hits[0].docID != "concept:self-attention" {
		t.Fatalf("alias seed did not resolve to canonical: %+v", leg.hits)
	}
	if aliases["concept:self-attention"] != "sdpa" {
		t.Errorf("alias_of missing: %v", aliases)
	}
}

// T4.3: empty ontology ⇒ byte-identical results to no ontology at all.
func TestRunEmptyOntologyByteIdentity(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)
	ont := ontology.NewStore(db, nil, nil) // zero entities

	ms.Add(memory.Entry{ID: "doc1", Content: "goroutines are lightweight"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "goroutines enable concurrency"},
	})

	req := Request{Query: "goroutines", Limit: 5}
	withOnt, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs, Ont: ont}, req)
	if err != nil {
		t.Fatal(err)
	}
	without, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs}, req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withOnt, without) {
		t.Errorf("empty ontology diverges from nil ontology:\n with: %+v\n without: %+v", withOnt, without)
	}
	if len(withOnt.Results) == 0 {
		t.Fatal("vacuous byte-identity — no results")
	}
}

// T4.3: a doc reachable ONLY through the graph surfaces with GraphRank and
// hydrated content; T4.4: channels excluding graph removes it.
func TestRunGraphChannelFusesAndAblates(t *testing.T) {
	deps, _ := graphFixture(t)

	// Only the self-attention entry matches lexically; transformer and
	// rnn are reachable only via the graph.
	resp, err := Run(context.Background(), deps, Request{Query: "self attention", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	byDoc := map[string]SearchResult{}
	for _, r := range resp.Results {
		byDoc[r.DocID] = r
	}
	tr, ok := byDoc["concept:transformer"]
	if !ok {
		t.Fatalf("graph-only doc missing from fused results: %+v", resp.Results)
	}
	if tr.GraphRank <= 0 {
		t.Errorf("graph-only doc lacks GraphRank: %+v", tr)
	}
	if tr.ChunkText == "" {
		t.Errorf("graph-only doc not hydrated: %+v", tr)
	}

	// Ablation: channels without graph must drop the graph-only doc.
	noGraph, err := Run(context.Background(), deps, Request{Query: "self attention", Limit: 10, Channels: []Channel{ChannelBM25, ChannelVector}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range noGraph.Results {
		if r.DocID == "concept:transformer" {
			t.Errorf("graph-only doc present with graph channel off: %+v", noGraph.Results)
		}
	}
}
