package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// witnessGraph builds the review's executed F-073 witness: with override
// {shortcut: 4.0}, b's cheapest ≤2-edge path is s→a→b (0.5) but b is
// discovered first via the expensive s-cites→b (1.4286) — a stale-score
// BFS kept the discovery score and its ranking depended on relation
// insertion order; the layered Bellman-Ford computes the exact bounded
// shortest scores regardless of order.
func witnessGraph(t *testing.T, edgesFirst bool) store.OntologyStore {
	t.Helper()
	db := openTestDB(t)
	ont := ontology.NewStore(db, nil, nil)
	for _, id := range []string{"s", "a", "b", "t-node", "u"} {
		if err := ont.AddEntity(ontology.Entity{ID: id, Type: "concept", Name: id, ArticlePath: "wiki/concepts/" + id + ".md"}); err != nil {
			t.Fatal(err)
		}
	}
	cheapFirst := []ontology.Relation{
		{ID: "r2", SourceID: "s", TargetID: "a", Relation: "shortcut"},
		{ID: "r1", SourceID: "s", TargetID: "b", Relation: "cites"},
	}
	expensiveFirst := []ontology.Relation{
		{ID: "r1", SourceID: "s", TargetID: "b", Relation: "cites"},
		{ID: "r2", SourceID: "s", TargetID: "a", Relation: "shortcut"},
	}
	head := cheapFirst
	if edgesFirst {
		head = expensiveFirst
	}
	rest := []ontology.Relation{
		{ID: "r3", SourceID: "a", TargetID: "b", Relation: "shortcut"},
		{ID: "r4", SourceID: "b", TargetID: "t-node", Relation: "shortcut"},
		{ID: "r5", SourceID: "s", TargetID: "u", Relation: "cites"},
	}
	for _, r := range append(head, rest...) {
		if err := ont.AddRelation(r); err != nil {
			t.Fatal(err)
		}
	}
	return ont
}

// F-073 regression. Contract: exact ≤2-edge shortest scores, insertion-
// order independent. Under the hop bound, b's true score is 0.5 (s→a→b,
// two cheap edges) not 1.4286 (the cites edge discovered first) — so b
// must outrank u (1.4286) under BOTH insertion orders, and the full
// ordering must be identical. t-node's only within-bound path is
// s→b→t (1.678, via the expensive discovery edge... the relaxed s→a→b→t
// is 3 edges, out of bounds), so t ranks last — asserting that too pins
// the hop bound against silently counting relaxed 3-edge paths.
func TestGraphLegShortestPathBothInsertionOrders(t *testing.T) {
	overrides := map[string]float64{"shortcut": 4.0}
	var orders [][]string
	for _, expensiveFirst := range []bool{false, true} {
		ont := witnessGraph(t, expensiveFirst)
		leg, _ := buildGraphLeg(ont, "s", 10, overrides)
		ids := make([]string, len(leg.hits))
		for i, h := range leg.hits {
			ids[i] = h.docID
		}
		orders = append(orders, ids)

		pos := map[string]int{}
		for i, id := range ids {
			pos[id] = i
		}
		bp, bok := pos["concept:b"]
		up, uok := pos["concept:u"]
		if !bok || !uok {
			t.Fatalf("expensiveFirst=%v: missing nodes: %v", expensiveFirst, pos)
		}
		if bp >= up {
			t.Errorf("expensiveFirst=%v: b (relaxed 2-edge score 0.5) ranked %d, u (1.4286) ranked %d — stale-score regression", expensiveFirst, bp, up)
		}
	}
	if !reflect.DeepEqual(orders[0], orders[1]) {
		t.Errorf("ranking depends on relation insertion order:\n first: %v\nsecond: %v", orders[0], orders[1])
	}
}

// F-074 regression: a PENDING alias proposal seeds neither the canonical
// nor an alias_of annotation.
func TestGraphLegPendingAliasSeedsNothing(t *testing.T) {
	deps, ont := graphFixture(t)
	os := ont.(*ontology.Store)
	if err := os.AddEntity(ontology.Entity{ID: "sdpa", Type: "concept", Name: "SDPA"}); err != nil {
		t.Fatal(err)
	}
	// A pending proposal: PutAlias records the row without linking.
	if err := os.PutAlias(store.EntityAlias{Alias: "sdpa", CanonicalID: "self-attention", Status: store.AliasPending}); err != nil {
		t.Fatal(err)
	}
	_ = deps
	leg, aliases := buildGraphLeg(ont, "sdpa internals", 10, nil)
	for _, h := range leg.hits {
		if h.docID == "concept:self-attention" {
			t.Errorf("pending alias seeded the canonical: %+v", leg.hits)
		}
	}
	if len(aliases) != 0 {
		t.Errorf("pending alias produced alias_of annotations: %v", aliases)
	}
}

// F-075 regression: a zero-weight override excludes the relation entirely.
func TestGraphLegZeroWeightExcludesRelation(t *testing.T) {
	deps, _ := graphFixture(t)
	leg, _ := buildGraphLeg(deps.Ont, "self attention", 10, map[string]float64{"cites": 0})
	for _, h := range leg.hits {
		if h.docID == "concept:transformer" {
			t.Errorf("cites excluded but its target surfaced: %+v", leg.hits)
		}
	}
}

// F-072 regression: graph-only docs reach the reranker with NON-EMPTY
// passages. A stub LLM captures the rerank prompt; every "[N] " candidate
// block must carry text.
func TestGraphOnlyDocsRerankWithContent(t *testing.T) {
	var mu sync.Mutex
	var prompts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if msgs, _ := body["messages"].([]any); len(msgs) > 0 {
			if m, ok := msgs[len(msgs)-1].(map[string]any); ok {
				if c, _ := m["content"].(string); c != "" {
					mu.Lock()
					prompts = append(prompts, c)
					mu.Unlock()
				}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": `[{"id":1,"score":8},{"id":2,"score":7},{"id":3,"score":6}]`}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 10},
		})
	}))
	defer server.Close()

	client, err := llm.NewClient("openai", "sk-test", server.URL, 0)
	if err != nil {
		t.Fatal(err)
	}

	deps, _ := graphFixture(t)
	deps.Client = client
	deps.Model = "gpt-4o-mini"

	// The fixture indexes chunks for none of the docs, so transformer and
	// rnn reach the candidate set only via the graph leg.
	resp, err := Run(context.Background(), deps, Request{Query: "self attention", Limit: 10, Rerank: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("no results")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) == 0 {
		t.Fatal("reranker was never called — test vacuous")
	}
	for _, p := range prompts {
		if hasEmptyPassage(p) {
			t.Errorf("rerank prompt contains an empty passage block:\n%s", p)
		}
	}
}

// hasEmptyPassage reports a numeric "[N]" block with no following text on
// the line (the JSON-example instruction line is not a passage block).
func hasEmptyPassage(prompt string) bool {
	for _, l := range splitLines(prompt) {
		if len(l) < 2 || l[0] != '[' {
			continue
		}
		close := -1
		digitsOnly := true
		for i := 1; i < len(l); i++ {
			if l[i] == ']' {
				close = i
				break
			}
			if l[i] < '0' || l[i] > '9' {
				digitsOnly = false
				break
			}
		}
		if close <= 1 || !digitsOnly {
			continue // not a "[N]" passage block
		}
		rest := ""
		for _, c := range l[close+1:] {
			if c != ' ' && c != '\t' {
				rest += string(c)
			}
		}
		if rest == "" {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
