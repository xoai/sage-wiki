package compiler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// resolveServer replies with a fixed clusters payload and records every prompt
// it was sent, so tests can assert what the model was actually asked.
func resolveServer(t *testing.T, payload string) (*httptest.Server, *atomic.Int64, *[]string) {
	t.Helper()
	var calls atomic.Int64
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		for _, m := range body.Messages {
			prompts = append(prompts, m.Content)
		}
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": payload}}},
			"model":   "m",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &prompts
}

func resolveCfg() *config.Config {
	c := &config.Config{}
	c.Ontology.Resolve.Enabled = true
	c.Ontology.Triples.Enabled = true
	c.Models.Extract = "m"
	return c
}

func addEnt(t *testing.T, s *ontology.Store, id, name, def, article string) {
	t.Helper()
	if err := s.AddEntity(ontology.Entity{
		ID: id, Type: ontology.TypeConcept, Name: name,
		Definition: def, ArticlePath: article,
	}); err != nil {
		t.Fatalf("AddEntity %s: %v", id, err)
	}
}

const twoMemberCluster = `{"clusters":[
  {"members":["E1","E2"],"same_referent":true,"broader":false,
   "confidence":0.95,"reason":"same astronaut"}
]}`

// Default-off is the opt-in contract: an upgrade must cost nothing.
func TestResolvePassDisabledMakesNoCall(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "an astronaut", "wiki/buzz.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 pilot", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, &config.Config{}, triplesClient(t, srv.URL), nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 when resolution is disabled", got)
	}
	if rows, _ := ont.ListAliases(store.AliasApplied); len(rows) != 0 {
		t.Errorf("alias rows = %d, want 0", len(rows))
	}
}

// A nil *Config must not panic: cfg.Ontology dereferences it, and the
// fullpipeline call site can hand over a partially-built config.
func TestResolvePassNilConfigDoesNotPanic(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	ResolveEntitiesPass(context.Background(), ont, []string{"a"}, nil, triplesClient(t, srv.URL), nil)
	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0", got)
	}
}

// A cancelled context must not buy a paid call. The pass is deferred to the end
// of runFullPipeline, so it runs on cancelled exits too.
func TestResolvePassCancelledContextMakesNoCall(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "an astronaut", "wiki/buzz.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 pilot", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ResolveEntitiesPass(ctx, ont, []string{"Buzz Aldrin"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 on a cancelled context", got)
	}
}

// The happy path: two ids for one person are linked, BOTH rows survive, and the
// canonical (the article-bearing row) carries the union of edges.
func TestResolvePassLinksVariants(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "", "wiki/concepts/buzz-aldrin.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 lunar module pilot", "")
	addEnt(t, ont, "apollo-11", "Apollo 11", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "Buzz Aldrin", TargetID: "apollo-11",
		Relation: ontology.RelExtends, Confidence: 0.8}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if got := calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}

	applied, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied aliases = %d, want 1: %+v", len(applied), applied)
	}
	// The article-bearing row is elected canonical.
	if applied[0].CanonicalID != "buzz-aldrin" || applied[0].Alias != "Buzz Aldrin" {
		t.Errorf("link = %s -> %s, want 'Buzz Aldrin' -> 'buzz-aldrin'",
			applied[0].Alias, applied[0].CanonicalID)
	}

	// BOTH entity rows still exist — linking never deletes.
	for _, id := range []string{"buzz-aldrin", "Buzz Aldrin"} {
		e, err := ont.GetEntity(id)
		if err != nil || e == nil {
			t.Errorf("entity %q was deleted by linking", id)
		}
	}
	// The canonical gained the alias's edge.
	canon, err := ont.GetRelations("buzz-aldrin", ontology.Outbound, "")
	if err != nil || len(canon) != 1 || canon[0].TargetID != "apollo-11" {
		t.Errorf("canonical did not gain the copied edge: %+v %v", canon, err)
	}
}

// broader=true routes to review no matter how confident the model is — this is
// the upstream over-merge failure mode.
func TestResolvePassBroaderGoesToReview(t *testing.T) {
	const broaderCluster = `{"clusters":[
	  {"members":["E1","E2"],"same_referent":true,"broader":true,
	   "confidence":0.99,"reason":"one is a programme"}]}`
	srv, _, _ := resolveServer(t, broaderCluster)
	ont := passStore(t)
	addEnt(t, ont, "gemini-12", "Gemini 12", "a crewed mission", "")
	addEnt(t, ont, "project-gemini", "Project Gemini", "a spaceflight programme", "wiki/pg.md")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"gemini-12"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if applied, _ := ont.ListAliases(store.AliasApplied); len(applied) != 0 {
		t.Errorf("applied = %d, want 0 — broader must not auto-link", len(applied))
	}
	pending, _ := ont.ListAliases(store.AliasPending)
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	if n, _ := ont.RelationCount(); n != 0 {
		t.Errorf("relations = %d, want 0 — a pending proposal copies nothing", n)
	}
}

// Name-only evidence never auto-links, whatever the confidence.
func TestResolvePassRequiresDescription(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "aldrin-a", "Buzz Aldrin", "", "wiki/a.md")
	addEnt(t, ont, "aldrin-b", "Buzz Aldrin", "", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-b"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if applied, _ := ont.ListAliases(store.AliasApplied); len(applied) != 0 {
		t.Errorf("applied = %d, want 0 — no description on either side", len(applied))
	}
	if pending, _ := ont.ListAliases(store.AliasPending); len(pending) != 1 {
		t.Errorf("pending = %d, want 1", len(pending))
	}
}

// same_referent=false is not a proposal at all.
func TestResolvePassDistinctEntitiesProduceNothing(t *testing.T) {
	const distinct = `{"clusters":[
	  {"members":["E1","E2"],"same_referent":false,"broader":false,
	   "confidence":0.9,"reason":"different people"}]}`
	srv, _, _ := resolveServer(t, distinct)
	ont := passStore(t)
	addEnt(t, ont, "armstrong-astronaut", "Neil Armstrong", "an astronaut", "")
	addEnt(t, ont, "armstrong-musician", "Louis Armstrong", "a trumpeter", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"armstrong-musician"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	for _, st := range []store.AliasStatus{store.AliasApplied, store.AliasPending} {
		if rows, _ := ont.ListAliases(st); len(rows) != 0 {
			t.Errorf("%s rows = %d, want 0", st, len(rows))
		}
	}
}

// A label the model omits is never touched — the pass acts only on what the
// model placed, so an entity cannot vanish because it was forgotten.
func TestResolvePassUnplacedLabelUntouched(t *testing.T) {
	const partial = `{"clusters":[
	  {"members":["E1","E2"],"same_referent":true,"broader":false,
	   "confidence":0.95,"reason":"same"}]}`
	srv, _, _ := resolveServer(t, partial)
	ont := passStore(t)
	addEnt(t, ont, "aldrin-a", "Buzz Aldrin", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "aldrin-b", "Edwin Aldrin", "an astronaut", "")
	addEnt(t, ont, "aldrin-c", "Aldrin Jr", "someone else entirely", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-b", "aldrin-c"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	// aldrin-c may appear in a block, but the model placed only E1/E2 in each
	// response, so it must never acquire an alias row.
	for _, st := range []store.AliasStatus{store.AliasApplied, store.AliasPending} {
		rows, _ := ont.ListAliases(st)
		for _, r := range rows {
			if r.Alias == "aldrin-c" || r.CanonicalID == "aldrin-c" {
				t.Errorf("an unplaced label was linked: %+v", r)
			}
		}
	}
	if e, _ := ont.GetEntity("aldrin-c"); e == nil {
		t.Error("unplaced entity was deleted")
	}
}

// The sweep re-applies applied rows with ZERO LLM calls, and runs even when
// arbitration is disabled — otherwise turning the feature off would silently
// stop the canonical staying complete.
func TestResolvePassSweepCopiesNewEdgesWithoutLLM(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "canon", "Canon", "the canonical", "wiki/canon.md")
	addEnt(t, ont, "alias", "Alias", "the alias", "")
	addEnt(t, ont, "target", "Target", "", "")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// An edge appears on the alias AFTER the link was applied.
	if err := ont.AddRelation(ontology.Relation{
		ID: "late", SourceID: "alias", TargetID: "target",
		Relation: ontology.RelExtends, Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}

	// Arbitration DISABLED — the sweep must still run.
	ResolveEntitiesPass(context.Background(), ont, nil, &config.Config{},
		triplesClient(t, srv.URL), nil)

	if got := calls.Load(); got != 0 {
		t.Errorf("LLM calls = %d, want 0 — the sweep is free", got)
	}
	canon, err := ont.GetRelations("canon", ontology.Outbound, "")
	if err != nil || len(canon) != 1 || canon[0].TargetID != "target" {
		t.Errorf("sweep did not copy the late edge onto the canonical: %+v %v", canon, err)
	}
}

// A pruned canonical is reported, not fatal, and the audit row survives so the
// link is still actionable if the entity returns.
func TestResolvePassSweepSurvivesPrunedCanonical(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "alias", "Alias", "", "")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "gone", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont, nil, &config.Config{},
		triplesClient(t, srv.URL), nil)

	rows, _ := ont.ListAliases(store.AliasApplied)
	if len(rows) != 1 {
		t.Errorf("audit row = %d, want 1 retained after a pruned canonical", len(rows))
	}
	if e, _ := ont.GetEntity("alias"); e == nil {
		t.Error("alias entity was removed")
	}
}

// A rejected pair must not be re-linked, even when a third entity pulls it back
// into a block. The step-8 candidate filter and the apply-time re-check are two
// different guards and both are needed.
func TestResolvePassRejectedPairNotRelinked(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "armstrong-a", "Neil Armstrong", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "armstrong-b", "Louis Armstrong", "a trumpeter", "")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "armstrong-b", CanonicalID: "armstrong-a", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"armstrong-b"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if applied, _ := ont.ListAliases(store.AliasApplied); len(applied) != 0 {
		t.Errorf("applied = %d, want 0 — the pair was rejected", len(applied))
	}
	rejected, _ := ont.ListAliases(store.AliasRejected)
	if len(rejected) != 1 || rejected[0].DecidedBy != "user" {
		t.Errorf("the rejection record was altered: %+v", rejected)
	}
}

// An entity that already carries an ACTIVE alias row from an earlier run must
// not be proposed again. A second active row is a NON-TARGET unique violation
// that ON CONFLICT (alias, canonical_id) does not absorb — it aborts the whole
// transaction and loses that run's edge copies, every run.
func TestResolvePassCrossRunActiveAliasNotReproposed(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "canon-1", "Buzz Aldrin", "an astronaut", "wiki/c1.md")
	addEnt(t, ont, "moved", "Buzz Aldrin", "an astronaut", "")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "moved", CanonicalID: "canon-1", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// A new seed that blocks with the already-linked entity.
	addEnt(t, ont, "seed", "Buzz Aldrin", "an astronaut", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"seed"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	// "moved" keeps exactly one active row, pointing where it did.
	act, err := ont.GetActiveAlias("moved")
	if err != nil {
		t.Fatal(err)
	}
	if act == nil || act.CanonicalID != "canon-1" {
		t.Errorf("existing link disturbed: %+v", act)
	}
}

// Cost attribution: without SetPass the spend bills to whatever ran last —
// "write", because the pass is deferred to the end of runFullPipeline.
func TestResolvePassRestoresCostAttribution(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a", "Buzz Aldrin", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "b", "Buzz Aldrin", "an astronaut", "")

	client := triplesClient(t, srv.URL)
	client.SetPass("write")
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"b"}, resolveCfg(), client, nil)

	if got := client.Pass(); got != "write" {
		t.Errorf("client pass = %q after the run, want the prior value restored", got)
	}
}
