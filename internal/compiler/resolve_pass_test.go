package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
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

// resolveCfg takes the threshold as a PARAMETER, deliberately. A test that
// inherited the package default would change meaning whenever the default
// moves: under the old 1.0 default every "no applied row" assertion held
// vacuously (canAutoApply returned false on its first line), and under 0.85
// the same fixture silently starts applying. Every caller here states the
// threshold it depends on.
//
// The one test that SHOULD inherit the default is
// TestResolvePassAutoAppliesByDefault, which builds its own config
// omitting the key: it exercises the default itself, not a guard.
func resolveCfg(threshold float64) *config.Config {
	c := &config.Config{}
	c.Ontology.Resolve.AutoApplyThreshold = threshold
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

// Confidence 1.0 is deliberate and load-bearing, from two sides. For the
// never-branch it is the exact input the branch defends against at an
// EXPLICIT threshold 1.0 — TestCanAutoApplyNeverAtThresholdOne is where
// removing the branch shows, and at 0.95 that mutation would survive because
// 0.95 < 1.0 queues anyway. For TestResolvePassAutoAppliesByDefault it clears
// the 0.85 default with no dependence on where between 0.85 and 1.0 the
// default might later sit.
const certainCluster = `{"clusters":[
  {"members":["E1","E2"],"same_referent":true,"broader":false,
   "confidence":1.0,"reason":"same astronaut"}
]}`

// Default-off is the opt-in contract: an upgrade must cost nothing.
func TestResolvePassDisabledMakesNoCall(t *testing.T) {
	srv, calls, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "an astronaut", "wiki/buzz.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 pilot", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, &config.Config{}, triplesClient(t, srv.URL), nil, nil)

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
	ResolveEntitiesPass(context.Background(), ont, []string{"a"}, nil, triplesClient(t, srv.URL), nil, nil)
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
	ResolveEntitiesPass(ctx, ont, []string{"Buzz Aldrin"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"Buzz Aldrin"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"gemini-12"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"aldrin-b"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"armstrong-musician"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"aldrin-b", "aldrin-c"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		triplesClient(t, srv.URL), nil, nil)

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
		triplesClient(t, srv.URL), nil, nil)

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
		[]string{"armstrong-b"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"seed"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"b"}, resolveCfg(0.85), client, nil, nil)

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
		resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"a-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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

	cfg := resolveCfg(0.85)
	cfg.Ontology.Resolve.UseEmbeddings = true
	emb := &stubEmbedder{fail: true}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"aldrin-b"}, cfg, triplesClient(t, srv.URL), emb, nil)

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

	cfg := resolveCfg(0.85)
	cfg.Ontology.Resolve.UseEmbeddings = true
	cfg.Ontology.Resolve.MaxEmbedCandidates = 5
	emb := &stubEmbedder{}

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"e0"}, cfg, triplesClient(t, srv.URL), emb, nil)

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
	cfg := resolveCfg(0.85)
	cfg.Ontology.Resolve.UseEmbeddings = true
	emb := &stubEmbedder{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ResolveEntitiesPass(ctx, ont, []string{"e0"}, cfg, triplesClient(t, srv.URL), emb, nil)

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
		[]string{"raw/2025/notes.md"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"aldrin-c"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"self-attention"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"a-row", "b-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"a-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"aldrin-t"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"a-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		[]string{"b-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

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
		triplesClient(t, srv.URL), nil, nil)

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
		[]string{"x-canon"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	for _, r := range applied {
		if r.Alias == "x-canon" {
			t.Errorf("wrote %s -> %s, but %s already resolves to %s — neither entity "+
				"is canonical and the sweep copies edges both ways forever",
				r.Alias, r.CanonicalID, r.CanonicalID, r.Alias)
		}
	}
}

// GATE-3 R6 CRITICAL 1. The pending gate checked only the DIRECT pair while the
// rejection gate beside it does the cluster cross product. A pending A->B is
// therefore consummated transitively by linking a third entity into the same
// component.
func TestResolvePassPendingPairNotConsummatedTransitively(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "A", "Buzz Aldrin", "an astronaut", "wiki/a.md")
	addEnt(t, ont, "B", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "T", "Buzz Aldrin", "an astronaut", "")
	addEnt(t, ont, "edge-tgt", "Apollo", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r", SourceID: "B", TargetID: "edge-tgt",
		Relation: ontology.RelExtends, Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}
	// B is already absorbed into T.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "B", CanonicalID: "T", EntityType: ontology.TypeConcept,
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	// A <-> B is awaiting a human.
	if err := ont.PutAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "B", EntityType: ontology.TypeConcept,
		Status: store.AliasPending, Confidence: 0.9, Source: "llm",
		CreatedAt: "2026-07-26T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// Seed T: cluster {T, A}. The direct pair (T, A) is not the pending pair,
	// but B sits in T's component, so linking T -> A merges A with B.
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"T"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

	applied, _ := ont.ListAliases(store.AliasApplied)
	for _, r := range applied {
		if r.Alias == "T" && r.CanonicalID == "A" {
			t.Errorf("linked T -> A, merging A with B transitively while A -> B is "+
				"still awaiting human review: %+v", r)
		}
	}
}

// GATE-3 R6 CRITICAL 2. The snapshot is loaded once per pass, but round 5 made
// it depend on PENDING rows — which the pass itself writes. A pair queued by an
// early block is invisible to a later block in the same run.
func TestResolvePassPendingWrittenThisRunIsHonoured(t *testing.T) {
	// Two blocks: the first queues a pending pair, the second must see it.
	const perBlock = `{"clusters":[
	  {"members":["E1","E2"],"same_referent":true,"broader":false,
	   "confidence":0.50,"reason":"unsure"}]}`
	srv, _, _ := resolveServer(t, perBlock)
	ont := passStore(t)
	// No descriptions -> confidence 0.5 is below threshold -> pending.
	addEnt(t, ont, "a-row", "Buzz Aldrin", "", "wiki/a.md")
	addEnt(t, ont, "b-row", "Buzz Aldrin", "", "wiki/b.md")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a-row", "b-row"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

	pending, _ := ont.ListAliases(store.AliasPending)
	applied, _ := ont.ListAliases(store.AliasApplied)
	// The same pair must not be both queued for review and linked in one run.
	for _, p := range pending {
		for _, a := range applied {
			if (p.Alias == a.Alias && p.CanonicalID == a.CanonicalID) ||
				(p.Alias == a.CanonicalID && p.CanonicalID == a.Alias) {
				t.Errorf("pair %s/%s is both pending and applied after one run",
					p.Alias, p.CanonicalID)
			}
		}
	}
}

// GATE-3 R7/R8. touchedSet is frozen from the seed list, but a seed can BECOME
// an alias in an earlier block — after which a later block's chain-resolved
// target was never touched by this compile, while touched[canonical.ID] is still
// true. The pre-R7 predicate then writes an APPLIED row for a pair neither side
// of which this compile looked at, deriving its edges and freezing it out of
// future resolution.
//
// My first attempt at this test was VACUOUS — one seed, therefore one block, so
// the "seed becomes an alias" mechanism never occurred, and the assertion held
// by construction. This fixture (from the Gate-3 round-8 reviewer) produces TWO
// blocks and is mutation-verified below to fail under the pre-R7 predicate.
//
//	block 1 (seed a): cluster {a,b} -> b elected (article) -> applied a -> b
//	                  ... so seed `a` is now an alias
//	block 2 (seed z): cluster {a,c} -> a elected -> target = CanonicalID(a) = b
//	                  touched[a] is true but touched[b] is NOT
func TestResolvePassDoesNotLeakThroughAWithinRunChain(t *testing.T) {
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "a", "Alpha Bravo", "", "")
	addEnt(t, ont, "b", "Alpha Golf", "the described canonical", "wiki/b.md")
	addEnt(t, ont, "c", "Delta Foxtrot", "", "")
	addEnt(t, ont, "z", "Bravo Delta", "", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"a", "z"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

	for _, st := range []store.AliasStatus{store.AliasApplied, store.AliasPending} {
		rows, _ := ont.ListAliases(st)
		for _, r := range rows {
			if r.Alias == "c" {
				t.Errorf("wrote %s -> %s (%s): neither endpoint was touched by this "+
					"compile — c is now frozen out of future resolution and its edges "+
					"were copied with no way to undo them", r.Alias, r.CanonicalID, r.Status)
			}
		}
	}
	// The primary use case must still land in the same run: the guard narrows
	// only the leak, not legitimate linking.
	applied, _ := ont.ListAliases(store.AliasApplied)
	found := false
	for _, r := range applied {
		if r.Alias == "a" && r.CanonicalID == "b" {
			found = true
		}
	}
	if !found {
		t.Errorf("the legitimate link a -> b did not happen; the guard over-blocks: %+v", applied)
	}
}

// GATE-3 R8 MAJOR. A candidate whose pair is already decided cannot produce a
// row — applyClusters discards it at the active-alias guard — so sending it to
// the model buys a call that is structurally incapable of returning anything.
// For an applied link that repeats on every compile, forever.
func TestResolvePassDoesNotArbitrateAlreadyDecidedPairs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status store.AliasStatus
	}{
		{"applied", store.AliasApplied},
		{"pending", store.AliasPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls, _ := resolveServer(t, twoMemberCluster)
			ont := passStore(t)
			addEnt(t, ont, "seed", "Buzz Aldrin", "an astronaut", "wiki/s.md")
			addEnt(t, ont, "alias-row", "Buzz Aldrin", "an astronaut", "")
			if err := ont.PutAlias(store.EntityAlias{
				Alias: "alias-row", CanonicalID: "seed", EntityType: ontology.TypeConcept,
				Status: tc.status, Confidence: 0.9, Source: "llm",
				CreatedAt: "2026-07-26T00:00:00Z",
			}); err != nil {
				t.Fatal(err)
			}

			ResolveEntitiesPass(context.Background(), ont,
				[]string{"seed"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

			if got := calls.Load(); got != 0 {
				t.Errorf("%d paid arbitration call(s) for a pair already %s — the only "+
					"candidate can never produce a row", got, tc.status)
			}
		})
	}
}

// GATE-3 R8. The exported entry point must normalise nil ctx/store the way the
// pass does; without it the first non-rejected applied row panics at ctx.Err().
func TestSweepAliasesNormalisesNilArguments(t *testing.T) {
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
	if err := ont.AddRelation(ontology.Relation{
		ID: "e", SourceID: "alias", TargetID: "tgt",
		Relation: ontology.RelExtends, Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}

	//nolint:staticcheck // deliberately passing a nil Context: this is the guard.
	res := SweepAliases(nil, ont)
	if res.Copied != 1 {
		t.Errorf("Copied = %d, want 1 — a nil ctx must be normalised, not panic", res.Copied)
	}
	if got := SweepAliases(context.Background(), nil); got != (SweepResult{}) {
		t.Errorf("a nil store must return a zero result, got %+v", got)
	}
}

// TestResolvePassAutoAppliesByDefault is the ONE test that inherits the
// package default, and it does so on purpose: it exercises the DEFAULT, not a
// guard, so §3.1's "no test may inherit" rule does not apply to it. Every other
// test states its threshold via resolveCfg.
//
// Without it the default is silently revertible — a later "safety branch" that
// restores review-only for users who never set the key leaves the rest of the
// suite green.
//
// The fixture passes EVERY guard: described on both sides, same_referent,
// broader false, confidence 1.0 ≥ 0.85. Under the default that MUST auto-apply.
// And because cheap mistakes are the argument for the 0.85 default, the same
// test walks the exit: unlink removes the derived edge and rejects the pair.
func TestResolvePassAutoAppliesByDefault(t *testing.T) {
	srv, calls, _ := resolveServer(t, certainCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "Apollo 11 pilot", "wiki/concepts/buzz-aldrin.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 lunar module pilot", "")
	addEnt(t, ont, "apollo-11", "Apollo 11", "", "")
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "Buzz Aldrin", TargetID: "apollo-11",
		Relation: ontology.RelExtends, Confidence: 0.8}); err != nil {
		t.Fatal(err)
	}

	// A config that OMITS auto_apply_threshold entirely — the whole point.
	cfg := &config.Config{}
	cfg.Ontology.Resolve.Enabled = true
	cfg.Ontology.Triples.Enabled = true
	cfg.Models.Extract = "m"

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, cfg, triplesClient(t, srv.URL), nil, nil)

	if got := calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}

	applied, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1 — the 0.85 default must auto-apply a "+
			"1.0-confidence fully-guarded cluster: %+v", len(applied), applied)
	}
	al := applied[0]
	// The articled row is the canonical, so the alias's edge derives onto it.
	if al.Alias != "Buzz Aldrin" || al.CanonicalID != "buzz-aldrin" {
		t.Fatalf("link direction = %q -> %q, want \"Buzz Aldrin\" -> \"buzz-aldrin\"",
			al.Alias, al.CanonicalID)
	}

	// The union: the canonical now shows the alias's apollo-11 edge, and the
	// whole-graph count includes the derived copy.
	rels, err := ont.GetRelations("buzz-aldrin", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	derived := false
	for _, r := range rels {
		if r.TargetID == "apollo-11" {
			derived = true
		}
	}
	if !derived {
		t.Errorf("canonical shows no derived edge to apollo-11 after auto-apply: %+v", rels)
	}
	if n, err := ont.RelationCount(); err != nil || n != 2 {
		t.Errorf("RelationCount = %d (err %v), want 2 (original + derived)", n, err)
	}

	// The round-trip: unlink deletes exactly the derived edge and rejects the
	// pair so the next compile cannot silently re-apply it.
	if err := ont.UnlinkAlias(al.Alias, al.CanonicalID); err != nil {
		t.Fatalf("UnlinkAlias: %v", err)
	}
	rels, err = ont.GetRelations("buzz-aldrin", ontology.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if r.TargetID == "apollo-11" {
			t.Errorf("derived edge survived unlink: %+v", r)
		}
	}
	if n, err := ont.RelationCount(); err != nil || n != 1 {
		t.Errorf("RelationCount after unlink = %d (err %v), want 1 — the original only", n, err)
	}
	rejected, err := ont.ListAliases(store.AliasRejected)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 {
		t.Errorf("rejected = %d, want 1 — unlink without rejection is a pause, not an undo", len(rejected))
	}
}

// --- T3: the backlog warning -------------------------------------------------
//
// A silent safety default is not a safety default. The pending count lives only
// in log.Info and internal/log defaults to LevelWarn, so without this warning a
// user upgrades, compiles, sees byte-identical output, and their graph stops
// linking.

// captureWarns swaps in a WARN-level logger and returns the accumulated output.
// Warn, not Debug, deliberately: the whole point is that the message reaches a
// user who did not pass -v.
func captureWarns(t *testing.T) func() string {
	t.Helper()
	var buf strings.Builder
	var mu sync.Mutex
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(
		&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(restore)
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// seedPending puts a pending row in the store WITHOUT the pass having created
// it — a standing backlog, which is the level the warning must report.
func seedPending(t *testing.T, ont store.OntologyStore, alias, canonical string) {
	t.Helper()
	if err := ont.PutAlias(store.EntityAlias{
		Alias: alias, CanonicalID: canonical, EntityType: "concept",
		Status: store.AliasPending, Source: "llm",
		CreatedAt: "2026-07-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed pending alias: %v", err)
	}
}

// errPendingStore fails ONLY the pending read. The sweep's AliasApplied read
// still works, so the test isolates the backlog query's error path.
type errPendingStore struct {
	store.OntologyStore
	err error
}

func (e errPendingStore) ListAliases(s store.AliasStatus) ([]store.EntityAlias, error) {
	if s == store.AliasPending {
		return nil, e.err
	}
	return e.OntologyStore.ListAliases(s)
}

// panicAppliedStore panics on the sweep's first statement.
type panicAppliedStore struct {
	store.OntologyStore
}

func (p panicAppliedStore) ListAliases(s store.AliasStatus) ([]store.EntityAlias, error) {
	if s == store.AliasApplied {
		panic("sweep exploded")
	}
	return p.OntologyStore.ListAliases(s)
}

// Row 5. The backlog is one THIS RUN DID NOT CREATE, and the run queues nothing.
// The qualifier is the test: a run that queues its own row passes under a
// per-run delta too, so without it nothing distinguishes the standing-backlog
// query from the stats.pending counter it replaced.
func TestResolvePassWarnsWhenProposalsPend(t *testing.T) {
	out := captureWarns(t)
	ont := passStore(t)
	seedPending(t, ont, "Old Alias", "old-canonical")

	ResolveEntitiesPass(context.Background(), ont, nil, resolveCfg(0.85), nil, nil, nil)

	got := out()
	if !strings.Contains(got, "--review") {
		t.Errorf("warning must name the command that drains the queue:\n%s", got)
	}
	if !strings.Contains(got, "pending=1") {
		t.Errorf("warning must report the standing count:\n%s", got)
	}
	// The message states what it CAN know. warnPendingBacklog takes only the
	// store — it cannot know what the run did, and under a lowered threshold a
	// run that auto-links would make this claim false.
	if strings.Contains(got, "nothing was linked") {
		t.Errorf("warning must not characterize what the run did:\n%s", got)
	}
}

// Row 6. The silent direction. Without it, `> 0` -> `>= 0` is green and the
// warning fires on every compile — the fastest way to make the default log
// level stop being the place a user reliably looks.
func TestResolvePassSilentWhenNothingPends(t *testing.T) {
	out := captureWarns(t)
	srv, _, _ := resolveServer(t, twoMemberCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "", "wiki/concepts/buzz-aldrin.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 lunar module pilot", "")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, resolveCfg(0.85), triplesClient(t, srv.URL), nil, nil)

	if got := out(); strings.Contains(got, "--review") {
		t.Errorf("no proposals pend, so nothing should ask for review:\n%s", got)
	}
}

// Row 7a. The exit that is silent PERMANENTLY if the defer sits below the
// Resolve.Enabled gate: a user turns resolve off with proposals standing, and
// those aliases stay frozen out of resolution with no signal ever again.
func TestResolvePassWarnsWhenResolveDisabled(t *testing.T) {
	out := captureWarns(t)
	ont := passStore(t)
	seedPending(t, ont, "Old Alias", "old-canonical")

	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, &config.Config{}, nil, nil, nil)

	if got := out(); !strings.Contains(got, "--review") {
		t.Errorf("disabling resolve must not silence a standing backlog:\n%s", got)
	}
}

// Row 7b. The ORDINARY case under `resolve: on, triples: off` —
// ExtractTriplesPass returns nil when triples is off, so touched comes only from
// Pass-3 articles, and an incremental compile where every concept dedup-merged
// writes none. Shares the single `if` at resolve_entities.go:576 with
// client == nil, so it covers that exit too.
func TestResolvePassWarnsWhenNothingTouched(t *testing.T) {
	out := captureWarns(t)
	ont := passStore(t)
	seedPending(t, ont, "Old Alias", "old-canonical")

	ResolveEntitiesPass(context.Background(), ont, nil, resolveCfg(0.85), nil, nil, nil)

	if got := out(); !strings.Contains(got, "--review") {
		t.Errorf("an empty touched set must not silence a standing backlog:\n%s", got)
	}
}

// Row 7c. The error path. Swallowing it with `pending, _ :=` makes a failed read
// indistinguishable from an empty queue — exactly the silence this whole item
// exists to prevent, and the constitution's principle 2 is no silent failures.
func TestResolvePassWarnsWhenBacklogQueryFails(t *testing.T) {
	out := captureWarns(t)
	ont := errPendingStore{OntologyStore: passStore(t), err: errors.New("boom")}

	ResolveEntitiesPass(context.Background(), ont, nil, resolveCfg(0.85), nil, nil, nil)

	got := out()
	if !strings.Contains(got, "boom") {
		t.Errorf("a failed backlog read must be reported, not swallowed:\n%s", got)
	}
}

// Row 7d. The detector for registering the defer BEFORE sweepAliases rather than
// after. sweepAliases' first statement is the AliasApplied read, so a panic
// there unwinds through the defer only if it was registered first.
func TestResolvePassWarnsWhenSweepPanics(t *testing.T) {
	out := captureWarns(t)
	base := passStore(t)
	seedPending(t, base, "Old Alias", "old-canonical")
	ont := panicAppliedStore{OntologyStore: base}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("fixture did not panic — the test would be vacuous")
			}
		}()
		ResolveEntitiesPass(context.Background(), ont, nil, resolveCfg(0.85), nil, nil, nil)
	}()

	if got := out(); !strings.Contains(got, "--review") {
		t.Errorf("a panic in the sweep must not lose the backlog warning:\n%s", got)
	}
}

// TestResolvePassEmitsEntityResolved (SPEC-07 §4): an auto-applied merge
// emits entity_resolved through the pass's sink — the seam the pipeline's
// FullPipelineOpts.Sink threads (QA F-001 wiring test).
func TestResolvePassEmitsEntityResolved(t *testing.T) {
	srv, calls, _ := resolveServer(t, certainCluster)
	ont := passStore(t)
	addEnt(t, ont, "buzz-aldrin", "Buzz Aldrin", "Apollo 11 pilot", "wiki/concepts/buzz-aldrin.md")
	addEnt(t, ont, "Buzz Aldrin", "Buzz Aldrin", "Apollo 11 lunar module pilot", "")

	cfg := &config.Config{}
	cfg.Ontology.Resolve.Enabled = true
	cfg.Ontology.Triples.Enabled = true
	cfg.Models.Extract = "m"

	sink := &resolveCaptureSink{}
	ResolveEntitiesPass(context.Background(), ont,
		[]string{"Buzz Aldrin"}, cfg, triplesClient(t, srv.URL), nil, nil, sink)

	if got := calls.Load(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	var found *events.EntityResolved
	for _, ev := range sink.events {
		if ev.Type == events.TypeEntityResolved {
			d := ev.Data.(events.EntityResolved)
			found = &d
		}
	}
	if found == nil {
		t.Fatalf("no entity_resolved event reached the sink (events: %d)", len(sink.events))
	}
	if found.Alias == "" || found.Canonical == "" {
		t.Errorf("payload = %+v, want alias + canonical", found)
	}
	if !found.Auto {
		t.Error("Auto = false, want true (auto-applied merge)")
	}
	if found.Confidence <= 0 {
		t.Errorf("Confidence = %v, want the cluster confidence", found.Confidence)
	}
}

type resolveCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *resolveCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}
