package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// The invariant: one alias keeps exactly ONE active row, and an earlier
	// link is never disturbed by a later run.
	//
	// Honest note on what enforces this. The partial unique index
	// idx_entity_aliases_active is the real mechanism — it rejects a second
	// active row for one alias, so the end state holds even if the Go guard in
	// applyClusters is removed (verified by mutation: the state assertions below
	// still pass, because the write is refused at the database and LinkAlias's
	// transaction rolls back). The Go guard is an optimisation: it avoids
	// attempting a doomed transaction and logging a failure per run. This test
	// therefore pins the INVARIANT, not the guard; dropping the index is what
	// would break it.
	//
	// The fixture is built so the cluster is genuinely {moved, seed} with a
	// DIFFERENT canonical than "moved" already has — "seed" holds the
	// ArticlePath, so it wins the election.
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	// Deliberately shares no name tokens, so it stays OUT of the block and
	// the cluster is genuinely {moved, seed} with seed elected.
	addEnt(t, ont, "canon-1", "Unrelated Original", "an astronaut", "")
	addEnt(t, ont, "moved", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "seed", "Buzz Aldrin", "an astronaut", "wiki/seed.md")
	addEnt(t, ont, "tgt", "Apollo", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r", SourceID: "seed", TargetID: "tgt",
		Relation: ontology.RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "moved", CanonicalID: "canon-1", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"seed"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	// "moved" keeps exactly one active row, still pointing at canon-1. Without
	// the guard this attempts a SECOND active row (moved -> seed), which is a
	// non-target unique-index violation that aborts the whole WriteTx.
	act, err := ont.GetActiveAlias("moved")
	if err != nil {
		t.Fatal(err)
	}
	if act == nil || act.CanonicalID != "canon-1" {
		t.Fatalf("existing link disturbed: %+v", act)
	}
	// Exactly one active row for "moved" — the index would have rejected a
	// second, taking the transaction with it.
	applied, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range applied {
		if a.Alias == "moved" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("active rows for 'moved' = %d, want 1", n)
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

// The ordering guard. WriteArticles (Pass 3) is what creates concept entity
// rows, so a resolution pass placed before it sees a pool without them and
// drops every touched id as "absent". This asserts the touched set carries a
// Pass-3 id and that the pass resolves against a pool containing it.
func TestResolvePassSeesPass3Entities(t *testing.T) {
	srv, calls, prompts := resolveServer(t, twoMemberCluster)
	ont := passStore(t)

	// The two rows the compiler really produces for one concept: the triples
	// form (description, no article) and the Pass-3 form (article, no
	// description, hyphen-slug id).
	addEnt(t, ont, "Self Attention", "Self Attention", "an attention mechanism", "")
	addEnt(t, ont, "self-attention", "Self Attention", "", "wiki/concepts/self-attention.md")

	// touched carries BOTH, as fullpipeline builds it: triples ids from Pass 2b
	// plus successful article concept names from Pass 3.
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Self Attention", "self-attention"},
		resolveCfg(), triplesClient(t, srv.URL), nil)

	if got := calls.Load(); got == 0 {
		t.Fatal("no arbitration call — the Pass-3 entity was not in the pool")
	}
	// The prompt must have carried both rows.
	joined := strings.Join(*prompts, "\n")
	if !strings.Contains(joined, "an attention mechanism") {
		t.Errorf("the described row was not offered to the model:\n%s", joined)
	}

	applied, _ := ont.ListAliases(store.AliasApplied)
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}
	// The article-bearing row wins the election.
	if applied[0].CanonicalID != "self-attention" {
		t.Errorf("canonical = %q, want the article-bearing row", applied[0].CanonicalID)
	}
	for _, id := range []string{"Self Attention", "self-attention"} {
		if e, _ := ont.GetEntity(id); e == nil {
			t.Errorf("entity %q deleted", id)
		}
	}
}

// GATE-3 CRITICAL regression. The rejection check ran against the ELECTED
// canonical, but the link is made to the CHAIN-RESOLVED target. When the
// elected canonical is itself an applied alias, the pair that actually gets
// linked was never checked — and because putAliasTx suppresses writes over a
// rejected row, the edges were copied with NO audit row, so the alias was
// re-seeded and re-copied on every subsequent compile, forever.
func TestResolvePassRejectionSurvivesChainResolution(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a-row", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "b-row", "Buzz Aldrin", "an astronaut", "wiki/b.md")
	addEnt(t, ont, "c-row", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "edge-target", "Apollo", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r", SourceID: "a-row", TargetID: "edge-target",
		Relation: ontology.RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}

	// b-row is itself an alias of c-row.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "b-row", CanonicalID: "c-row", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// The user rejected a-row <-> c-row specifically.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "c-row", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a-row"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	// c-row must NOT have acquired the rejected entity's edge.
	got, err := ont.GetRelations("c-row", ontology.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("the rejected pair was linked through the chain: c-row gained %+v", got)
	}
	// And the rejection record is intact.
	rej, _ := ont.ListAliases(store.AliasRejected)
	if len(rej) != 1 || rej[0].DecidedBy != "user" {
		t.Errorf("rejection record altered: %+v", rej)
	}
}

// GATE-3 MAJOR regression. A suppressed audit write must not be reported as a
// successful link: the graph would mutate with nothing recording it.
func TestLinkAliasFailsWhenAuditWriteSuppressed(t *testing.T) {
	ont := passStore(t)
	addEnt(t, ont, "alias", "Alias", "", "")
	addEnt(t, ont, "canon", "Canon", "", "")
	addEnt(t, ont, "tgt", "Target", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r", SourceID: "alias", TargetID: "tgt",
		Relation: ontology.RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := ont.LinkAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	})
	if err == nil {
		t.Fatal("LinkAlias over a rejected row returned nil; the audit write was suppressed")
	}
	// And the whole transaction rolled back — no orphan copy.
	canon, _ := ont.GetRelations("canon", ontology.Outbound, "")
	if len(canon) != 0 {
		t.Errorf("edges were copied despite the suppressed audit row: %+v", canon)
	}
}

// --- GATE-3: coverage the reviewer found missing ---

type stubEmbedder struct {
	calls int
	fail  bool
	vec   map[string][]float32
}

func (s *stubEmbedder) Embed(text string) ([]float32, error) {
	s.calls++
	if s.fail {
		return nil, fmt.Errorf("embed outage")
	}
	for k, v := range s.vec {
		if strings.Contains(text, k) {
			return v, nil
		}
	}
	return []float32{0, 0, 1}, nil
}
func (s *stubEmbedder) Dimensions() int { return 3 }
func (s *stubEmbedder) Name() string    { return "stub" }

// An embedding outage must not cost the vault its resolution: lexical blocking
// stands on its own.
func TestResolvePassEmbeddingFailureFallsBackToLexical(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "aldrin-a", "Buzz Aldrin", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "aldrin-b", "Buzz Aldrin", "an astronaut", "")

	cfg := resolveCfg()
	cfg.Ontology.Resolve.UseEmbeddings = true
	emb := &stubEmbedder{fail: true}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-b"}, cfg, triplesClient(t, srv.URL), emb)

	if emb.calls == 0 {
		t.Error("the embedder was never called despite use_embeddings")
	}
	// Lexical blocking still found the pair, so arbitration still happened.
	if calls.Load() != 1 {
		t.Errorf("LLM calls = %d, want 1 — lexical blocking must survive an embed outage", calls.Load())
	}
	if applied, _ := ont.ListAliases(store.AliasApplied); len(applied) != 1 {
		t.Errorf("applied = %d, want 1", len(applied))
	}
}

// The global per-run embed cap bounds spend; embed.Embedder has no batch method,
// so every vector is one HTTP call.
func TestResolvePassEmbedCapIsGlobal(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	for i := 0; i < 30; i++ {
		addEnt(t, ont, fmt.Sprintf("e%d", i), fmt.Sprintf("Buzz Aldrin %d", i), "an astronaut", "")
	}

	cfg := resolveCfg()
	cfg.Ontology.Resolve.UseEmbeddings = true
	cfg.Ontology.Resolve.MaxEmbedCandidates = 5
	emb := &stubEmbedder{}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"e0"}, cfg, triplesClient(t, srv.URL), emb)

	if emb.calls > 5 {
		t.Errorf("embed calls = %d, want <= the global cap of 5", emb.calls)
	}
}

// A cancelled context stops the embed loop rather than paying for every vector.
func TestResolvePassEmbedLoopChecksContext(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	for i := 0; i < 20; i++ {
		addEnt(t, ont, fmt.Sprintf("e%d", i), fmt.Sprintf("Buzz Aldrin %d", i), "an astronaut", "")
	}
	cfg := resolveCfg()
	cfg.Ontology.Resolve.UseEmbeddings = true
	emb := &stubEmbedder{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ResolveEntitiesPass(ctx, ont, []string{"e0"}, cfg, triplesClient(t, srv.URL), emb)

	if emb.calls != 0 {
		t.Errorf("embed calls = %d on a cancelled context, want 0", emb.calls)
	}
}

// Two source entities with the same basename are indistinguishable and must
// never be linked — doing so would re-point one document's citations at another.
func TestResolvePassSourceTypeExcluded(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	for _, id := range []string{"raw/2024/notes.md", "raw/2025/notes.md"} {
		if err := ont.AddEntity(ontology.Entity{
			ID: id, Type: ontology.TypeSource, Name: "notes.md",
		}); err != nil {
			t.Fatal(err)
		}
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"raw/2025/notes.md"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	if calls.Load() != 0 {
		t.Errorf("LLM calls = %d, want 0 — source entities are never resolved", calls.Load())
	}
	for _, st := range []store.AliasStatus{store.AliasApplied, store.AliasPending} {
		if rows, _ := ont.ListAliases(st); len(rows) != 0 {
			t.Errorf("%s rows = %d, want 0", st, len(rows))
		}
	}
}

// An incremental run must never modify an entity it did not touch.
func TestResolvePassIncrementalLeavesUntouchedRowsAlone(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "aldrin-a", "Buzz Aldrin", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "aldrin-b", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "aldrin-c", "Buzz Aldrin", "an astronaut", "")

	before := map[string]ontology.Entity{}
	for _, id := range []string{"aldrin-a", "aldrin-b", "aldrin-c"} {
		e, err := ont.GetEntity(id)
		if err != nil || e == nil {
			t.Fatal(err)
		}
		before[id] = *e
	}

	// Only aldrin-c is touched.
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-c"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	// Whatever was linked, only the TOUCHED entity may have acquired an alias
	// row, and no untouched entity row may have changed.
	for _, id := range []string{"aldrin-a", "aldrin-b"} {
		after, err := ont.GetEntity(id)
		if err != nil || after == nil {
			t.Fatalf("untouched entity %q vanished", id)
		}
		if *after != before[id] {
			t.Errorf("untouched entity %q was modified:\n before %+v\n after  %+v", id, before[id], *after)
		}
		if act, _ := ont.GetActiveAlias(id); act != nil {
			t.Errorf("untouched entity %q was absorbed as an alias: %+v", id, act)
		}
	}
}

// GATE-3 R2 MAJOR. The primary use case: an article row written THIS compile and
// a triples row written by an EARLIER one. The article row wins the election
// (ArticlePath beats everything), so the alias is the untouched triples row —
// and a per-alias touched guard would skip it, linking nothing, queuing nothing,
// and re-billing an arbitration call on every subsequent compile.
func TestResolvePassLinksAgainstEntityFromAnEarlierCompile(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	// Written by an earlier compile — NOT in touched.
	addEnt(t, ont, "Self Attention", "Self Attention", "an attention mechanism", "")
	addEnt(t, ont, "tgt", "Transformers", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "old", SourceID: "Self Attention", TargetID: "tgt",
		Relation: ontology.RelExtends, Confidence: 0.6}); err != nil {
		t.Fatal(err)
	}
	// Written by THIS compile.
	addEnt(t, ont, "self-attention", "Self Attention", "", "wiki/concepts/self-attention.md")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"self-attention"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	pending, _ := ont.ListAliases(store.AliasPending)
	if len(applied)+len(pending) == 0 {
		t.Fatalf("the pair was neither linked nor queued: applied=%d pending=%d, "+
			"and the arbitration call (%d) will be repeated every compile",
			len(applied), len(pending), calls.Load())
	}
	if len(applied) == 1 {
		// The canonical must have gained the older row's edge.
		canon, _ := ont.GetRelations("self-attention", ontology.Outbound, "")
		if len(canon) != 1 || canon[0].TargetID != "tgt" {
			t.Errorf("canonical did not gain the earlier row's edge: %+v", canon)
		}
	}
}

// GATE-3 R2 CRITICAL. Both halves of a user-rejected pair must not be folded
// into one canonical, even across separate blocks in the same run — that
// reconstructs exactly the merge the user refused.
func TestResolvePassRejectedPairNotCoAbsorbedAcrossBlocks(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a-row", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "b-row", "Buzz Aldrin", "a jazz musician", "")
	addEnt(t, ont, "c-canon", "Buzz Aldrin", "an astronaut", "wiki/c.md")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "b-row", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a-row", "b-row"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	intoC := 0
	for _, a := range applied {
		if a.CanonicalID == "c-canon" && (a.Alias == "a-row" || a.Alias == "b-row") {
			intoC++
		}
	}
	if intoC > 1 {
		t.Errorf("both halves of a rejected pair were folded into %q: %+v", "c-canon", applied)
	}
}

// GATE-3 R3 CRITICAL. The sibling index was keyed by each row's DIRECT
// canonical but queried with the CHAIN-RESOLVED target, so an alias sitting
// under an intermediate hop was invisible — and multi-hop chains are the normal
// steady state, because LinkAlias never rewrites rows to the terminal.
func TestResolvePassRejectedPairNotCoAbsorbedThroughAChain(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	for _, id := range []string{"a-row", "x-row", "y-row", "z-row"} {
		addEnt(t, ont, id, "Buzz Aldrin", "an astronaut", "")
	}
	// z-row wins the election.
	if err := ont.UpdateEntity(ontology.Entity{
		ID: "z-row", Name: "Buzz Aldrin", Definition: "an astronaut",
		ArticlePath: "wiki/z.md"}); err != nil {
		t.Fatal(err)
	}
	// x -> y -> z, applied, never rewritten to the terminal.
	for _, p := range [][2]string{{"x-row", "y-row"}, {"y-row", "z-row"}} {
		if err := ont.PutAlias(store.EntityAlias{
			Alias: p[0], CanonicalID: p[1], EntityType: ontology.TypeConcept,
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The user separated a-row from x-row, which now sits under z-row.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "x-row", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a-row"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	for _, a := range applied {
		if a.Alias == "a-row" {
			t.Errorf("a-row was linked to %q, joining x-row transitively under the same "+
				"canonical despite the user separating them", a.CanonicalID)
		}
	}
}

// GATE-3 R3 MAJOR. A pair where NEITHER side was touched must not be decided:
// the untouched entity acquires an alias row, which permanently removes it from
// future resolution (resolvableSeeds skips ids with an active row).
func TestResolvePassDoesNotDecideAboutTwoUntouchedEntities(t *testing.T) {
	const threeMember = `{"clusters":[
	  {"members":["E1","E2","E3"],"same_referent":true,"broader":false,
	   "confidence":0.95,"reason":"same"}]}`
	srv, _, _ := resolveServer(t, threeMember)
	ont := passStore(t)
	addEnt(t, ont, "aldrin-t", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "aldrin-u1", "Buzz Aldrin", "an astronaut", "wiki/u1.md")
	addEnt(t, ont, "aldrin-u2", "Buzz Aldrin", "an astronaut", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-t"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	for _, st := range []store.AliasStatus{store.AliasApplied, store.AliasPending} {
		rows, _ := ont.ListAliases(st)
		for _, r := range rows {
			if r.Alias == "aldrin-u2" {
				t.Errorf("a row was written for %q, which this compile never touched, "+
					"freezing it out of future resolution: %+v", r.Alias, r)
			}
		}
	}
	// The touched entity's own pair is still decided.
	act, _ := ont.GetActiveAlias("aldrin-t")
	if act == nil {
		t.Error("the touched entity's pair was not decided")
	}
}

// GATE-3 R3 MAJOR. When the chain-resolved target cannot be loaded, the pair
// must be QUEUED, not silently lost — canAutoApply is an OR over both sides, so
// a described alias would otherwise still auto-apply.
func TestResolvePassUnloadableTargetGoesToReview(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a-row", "Buzz Aldrin", "a described astronaut", "")
	addEnt(t, ont, "b-row", "Buzz Aldrin", "", "wiki/b.md")
	// b-row points at a canonical that no longer exists (pruned).
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "b-row", CanonicalID: "c-gone", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a-row"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	pending, _ := ont.ListAliases(store.AliasPending)
	found := false
	for _, r := range append(applied, pending...) {
		if r.Alias == "a-row" {
			found = true
			if r.Status == store.AliasApplied {
				t.Errorf("auto-applied against an unloadable target: %+v", r)
			}
			// The row must name an entity that EXISTS. A row naming the pruned
			// ghost can never be applied (--apply always errors) while its
			// active status permanently freezes the alias out of resolution.
			if r.CanonicalID == "c-gone" {
				t.Errorf("queued against the pruned ghost %q: the row is unapplicable "+
					"and freezes %q out of future resolution", r.CanonicalID, r.Alias)
			}
			if e, _ := ont.GetEntity(r.CanonicalID); e == nil {
				t.Errorf("queued against %q, which does not exist", r.CanonicalID)
			}
		}
	}
	if !found {
		t.Error("the proposal was silently lost; the call will be re-billed every compile")
	}
}

// GATE-3 R5 CRITICAL. A pair awaiting human review must not be auto-applied by
// re-rolling the direction. Run 1 queues a -> b (no description, so it fails the
// auto-apply bar); run 2 gives `a` an article so it wins the election, making
// the alias `b`, which has no row of its own — every guard passes and the pass
// links the very pair a human was asked about.
func TestResolvePassDoesNotAutoApplyAPairAwaitingReview(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a-row", "Buzz Aldrin", "", "")
	addEnt(t, ont, "b-row", "Buzz Aldrin", "a described astronaut", "")

	// Run 1 already queued the pair for a human.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "a-row", CanonicalID: "b-row", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.95, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// Run 2: a-row acquires an article, so it now wins electCanonical.
	if err := ont.AddEntity(ontology.Entity{
		ID: "a-row", Type: ontology.TypeConcept, Name: "Buzz Aldrin",
		ArticlePath: "wiki/a.md"}); err != nil {
		t.Fatal(err)
	}

	// b-row is the seed: it has no active row, so resolvableSeeds keeps it.
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"b-row"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	for _, r := range applied {
		if r.Alias == "b-row" && r.CanonicalID == "a-row" {
			t.Errorf("auto-applied %s -> %s, the reverse of a pair already queued "+
				"for human review — the review bar was bypassed by re-rolling the direction",
				r.Alias, r.CanonicalID)
		}
	}
}

// GATE-3 R5. The sweep must not keep copying edges across a pair the user has
// rejected. It lists applied rows and re-links them unconditionally.
func TestResolveSweepHonoursRejections(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "alias", "Alias", "", "")
	addEnt(t, ont, "canon", "Canon", "", "")
	addEnt(t, ont, "tgt", "Target", "", "")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// The user then rejects the pair in the other direction.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "canon", CanonicalID: "alias", EntityType: ontology.TypeConcept,
		Status: store.AliasRejected, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		DecidedBy: "user",
	}); err != nil {
		t.Fatal(err)
	}
	// A new edge lands on the alias afterwards.
	if err := ont.AddRelation(ontology.Relation{
		ID: "late", SourceID: "alias", TargetID: "tgt",
		Relation: ontology.RelExtends, Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont, nil, &config.Config{},
		triplesClient(t, srv.URL), nil)

	canon, _ := ont.GetRelations("canon", ontology.Outbound, "")
	if len(canon) != 0 {
		t.Errorf("the sweep copied an edge across a rejected pair: %+v", canon)
	}
}

// No cycle is created when the elected canonical resolves back to the alias.
//
// Reachable state: Y is an applied alias of X but Y holds the ArticlePath, so Y
// wins electCanonical while resolving back to X. X is a legal seed (a canonical
// has no alias row of its own).
//
// Honest note on the mechanism, verified by mutation: LinkAlias's own
// self-alias guard (aliases.go) is what enforces this — removing the pass's
// target==alias check leaves the end state identical, because LinkAlias refuses
// the write and the pass logs an error instead. The pass check is
// defence-in-depth that avoids a spurious failure per run. This test pins the
// INVARIANT; the store guard is what would break it.
func TestResolvePassDoesNotCreateACycle(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "x-canon", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "y-alias", "Buzz Aldrin", "an astronaut", "wiki/y.md")
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "y-alias", CanonicalID: "x-canon", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"x-canon"}, resolveCfg(), triplesClient(t, srv.URL), nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	for _, r := range applied {
		if r.Alias == "x-canon" {
			t.Errorf("wrote %s -> %s, but %s already resolves to %s — neither entity "+
				"is canonical and the sweep copies edges both ways forever",
				r.Alias, r.CanonicalID, r.CanonicalID, r.Alias)
		}
	}
}
