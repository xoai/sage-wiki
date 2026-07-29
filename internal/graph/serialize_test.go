package graph

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// fakeOntStore is a partial fake (the erroringChain precedent): it embeds the
// interface and implements only the three methods SerializeSubgraph touches.
// It exists because the REAL store cannot host some spec fixtures —
// relations.id is TEXT PRIMARY KEY on both backends, so two empty-ID rows
// can only reach a reader as COALESCE'd legacy NULLs, which this models.
type fakeOntStore struct {
	store.OntologyStore
	rels     map[string][]store.Relation
	entities map[string]*store.Entity
}

func (f *fakeOntStore) GetRelations(id string, _ store.Direction, _ string) ([]store.Relation, error) {
	return f.rels[id], nil
}
func (f *fakeOntStore) GetRelationsAt(id string, _ store.Direction, _ string, _ time.Time) ([]store.Relation, error) {
	return f.rels[id], nil
}
func (f *fakeOntStore) GetEntity(id string) (*store.Entity, error) {
	return f.entities[id], nil // nil, nil for missing — the real store's contract
}
func (f *fakeOntStore) CanonicalID(id string) (string, error) { return id, nil }

// TestSerializeRenderFormat pins the whole render contract with goldens on
// Text (NOT SerializedEdge.Line — Line is the bare triple without the braces
// tag, and a Line-based golden would never see this contract). Four rows:
// all-fields (pins evidence-after-confidence placement), zero-confidence
// (field omitted — 0 means "not scored" on keyword edges, printing 0.00
// reads as a claim), evidence-only, all-empty (no braces).
func TestSerializeRenderFormat(t *testing.T) {
	f := &fakeOntStore{
		rels: map[string][]store.Relation{
			"a": {
				{SourceID: "a", TargetID: "b1", Relation: "extends",
					SourceDoc: "raw/a.md", Confidence: 0.9, Evidence: "quoted span"},
				{SourceID: "a", TargetID: "b2", Relation: "extends",
					SourceDoc: "raw/b.md"},
				{SourceID: "a", TargetID: "b3", Relation: "extends",
					Evidence: "solo evidence"},
				{SourceID: "a", TargetID: "b4", Relation: "extends"},
			},
		},
		entities: map[string]*store.Entity{
			"a":  {ID: "a", Name: "Alpha"},
			"b1": {ID: "b1", Name: "BeeOne"},
			"b2": {ID: "b2", Name: "BeeTwo"},
			// b3, b4 deliberately missing: ids must be used as names.
		},
	}

	got, err := SerializeSubgraph(f, []string{"a"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`E1: (Alpha) --[extends]--> (BeeOne) {source: raw/a.md, confidence: 0.90, evidence: "«quoted span»"}`,
		`E2: (Alpha) --[extends]--> (BeeTwo) {source: raw/b.md}`,
		`E3: (Alpha) --[extends]--> (b3) {evidence: "«solo evidence»"}`,
		`E4: (Alpha) --[extends]--> (b4)`,
	}, "\n")
	if got.Text != want {
		t.Errorf("render contract broken:\n got:\n%s\nwant:\n%s", got.Text, want)
	}
	if got.Truncated {
		t.Errorf("Truncated = true on an uncapped fixture")
	}
}

// TestSerializeDedupesByTriple: edge identity is (SourceID, Relation,
// TargetID), NOT Rel.ID — two DISTINCT triples both carrying the empty ID
// (the COALESCE'd-legacy-NULL shape) must BOTH survive. Dedupe-by-ID
// collapses them to one.
func TestSerializeDedupesByTriple(t *testing.T) {
	f := &fakeOntStore{
		rels: map[string][]store.Relation{
			"a": {
				{ID: "", SourceID: "a", TargetID: "b", Relation: "extends"},
				{ID: "", SourceID: "a", TargetID: "c", Relation: "extends"},
			},
		},
		entities: map[string]*store.Entity{},
	}
	got, err := SerializeSubgraph(f, []string{"a"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Edges) != 2 {
		t.Errorf("edges = %d, want 2 — distinct triples with equal (empty) IDs must both survive:\n%s",
			len(got.Edges), got.Text)
	}
}

// erroringEntityStore fails every GetEntity call — a DB error, not a
// missing row.
type erroringEntityStore struct {
	store.OntologyStore
	rels map[string][]store.Relation
}

func (f *erroringEntityStore) GetRelations(id string, _ store.Direction, _ string) ([]store.Relation, error) {
	return f.rels[id], nil
}
func (f *erroringEntityStore) GetRelationsAt(id string, _ store.Direction, _ string, _ time.Time) ([]store.Relation, error) {
	return f.rels[id], nil
}
func (f *erroringEntityStore) GetEntity(string) (*store.Entity, error) {
	return nil, errors.New("db handle poisoned")
}
func (f *erroringEntityStore) CanonicalID(id string) (string, error) { return id, nil }

// TestSerializeWarnsOnEntityLookupError: a real GetEntity ERROR must not be
// silently conflated with a missing row (no-silent-failures principle) —
// the id fallback still renders, but a WARN says why names degraded.
func TestSerializeWarnsOnEntityLookupError(t *testing.T) {
	var buf strings.Builder
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(&buf,
		&slog.HandlerOptions{Level: slog.LevelWarn})))
	defer restore()

	f := &erroringEntityStore{rels: map[string][]store.Relation{
		"a": {{SourceID: "a", TargetID: "b", Relation: "extends"}},
	}}
	got, err := SerializeSubgraph(f, []string{"a"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "(a) --[extends]--> (b)") {
		t.Errorf("id fallback must still render on lookup error:\n%s", got.Text)
	}
	if !strings.Contains(buf.String(), "entity lookup failed") {
		t.Errorf("a lookup ERROR must warn — silence conflates it with a missing row; log: %q", buf.String())
	}
}

// serializeStore builds a real-store fixture. Relations carry explicit
// CreatedAt values where determinism needs them.
func serializeStore(t *testing.T, ents []ontology.Entity, rels []ontology.Relation) *ontology.Store {
	t.Helper()
	s := setupTestStore(t)
	for _, e := range ents {
		if err := s.AddEntity(e); err != nil {
			t.Fatalf("AddEntity %s: %v", e.ID, err)
		}
	}
	for _, r := range rels {
		if err := s.AddRelation(r); err != nil {
			t.Fatalf("AddRelation %s->%s: %v", r.SourceID, r.TargetID, err)
		}
	}
	return s
}

func textHasLine(text, frag string) bool {
	return strings.Contains(text, frag)
}

// TestSerializeHopBounded pins the EXCLUSION side of MaxHops: a chain
// A→B→C at MaxHops=1 serializes only A→B. A BFS that ignores MaxHops also
// emits B→C and is red. (The always-one-hop mutant is caught by
// TestSerializeMultiHopAcrossDocs, whose fixture REQUIRES hop 2.)
func TestSerializeHopBounded(t *testing.T) {
	s := serializeStore(t,
		[]ontology.Entity{
			{ID: "A", Type: "concept", Name: "A"},
			{ID: "B", Type: "concept", Name: "B"},
			{ID: "C", Type: "concept", Name: "C"},
		},
		[]ontology.Relation{
			{ID: "r1", SourceID: "A", TargetID: "B", Relation: "extends"},
			{ID: "r2", SourceID: "B", TargetID: "C", Relation: "extends"},
		})
	got, err := SerializeSubgraph(s, []string{"A"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !textHasLine(got.Text, "(A) --[extends]--> (B)") {
		t.Errorf("hop-1 edge missing:\n%s", got.Text)
	}
	if textHasLine(got.Text, "(C)") {
		t.Errorf("MaxHops=1 must not reach C:\n%s", got.Text)
	}
}

// TestSerializeMultiHopAcrossDocs: the mission is the seed, so reaching the
// location REQUIRES the second hop through the alias-unified person. Expect
// THREE lines, by membership — three is CORRECT, not a bug: the alias's
// original edge and its derived copy onto the canonical are distinct triples
// that triple-dedupe keeps. Both source_docs must survive serialization.
func TestSerializeMultiHopAcrossDocs(t *testing.T) {
	s := serializeStore(t,
		[]ontology.Entity{
			{ID: "apollo-11", Type: "concept", Name: "Apollo 11"},
			{ID: "buzz-aldrin", Type: "concept", Name: "Buzz Aldrin", ArticlePath: "wiki/buzz.md"},
			{ID: "Buzz Aldrin", Type: "concept", Name: "Buzz Aldrin"},
			{ID: "moon", Type: "concept", Name: "Moon"},
		},
		[]ontology.Relation{
			// doc A asserts the ALIAS's edge; LinkAlias derives it onto the canonical.
			{ID: "rA", SourceID: "Buzz Aldrin", TargetID: "apollo-11", Relation: "extends",
				SourceDoc: "raw/docA.md", Confidence: 0.8},
			// doc B asserts the CANONICAL's edge.
			{ID: "rB", SourceID: "buzz-aldrin", TargetID: "moon", Relation: "extends",
				SourceDoc: "raw/docB.md", Confidence: 0.7},
		})
	if _, err := s.LinkAlias(store.EntityAlias{
		Alias: "Buzz Aldrin", CanonicalID: "buzz-aldrin", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := SerializeSubgraph(s, []string{"apollo-11"}, SubgraphOpts{MaxHops: 2, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{
		"(Buzz Aldrin) --[extends]--> (Apollo 11)", // original AND derived copy render alike by name...
		"(Buzz Aldrin) --[extends]--> (Moon)",      // ...the hop-2 edge, doc B
		"source: raw/docA.md",
		"source: raw/docB.md",
	} {
		if !textHasLine(got.Text, frag) {
			t.Errorf("missing %q:\n%s", frag, got.Text)
		}
	}
	if len(got.Edges) != 3 {
		t.Errorf("edges = %d, want 3 (alias original + derived copy + hop-2) — do not dedupe the copy away:\n%s",
			len(got.Edges), got.Text)
	}
}

// TestSerializeBounded: within each hop edges are sorted BEFORE the cap
// applies, so the retained SET is deterministic — the assertion is
// membership, not just count.
func TestSerializeBounded(t *testing.T) {
	s := serializeStore(t,
		[]ontology.Entity{
			{ID: "A", Type: "concept", Name: "A"},
			{ID: "b", Type: "concept", Name: "B1"},
			{ID: "c", Type: "concept", Name: "C1"},
			{ID: "d", Type: "concept", Name: "D1"},
		},
		[]ontology.Relation{
			// inserted in reverse target order: the cap must retain the
			// SORTED-first two (b, c), not the first-inserted two (d, c).
			{ID: "r3", SourceID: "A", TargetID: "d", Relation: "extends"},
			{ID: "r2", SourceID: "A", TargetID: "c", Relation: "extends"},
			{ID: "r1", SourceID: "A", TargetID: "b", Relation: "extends"},
		})
	got, err := SerializeSubgraph(s, []string{"A"}, SubgraphOpts{MaxHops: 1, MaxEdges: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Errorf("Truncated = false with 3 edges and MaxEdges 2")
	}
	if len(got.Edges) != 2 {
		t.Fatalf("edges = %d, want 2:\n%s", len(got.Edges), got.Text)
	}
	if !textHasLine(got.Text, "(B1)") || !textHasLine(got.Text, "(C1)") {
		t.Errorf("cap must retain the sorted-first edges (b, c):\n%s", got.Text)
	}
	if textHasLine(got.Text, "(D1)") {
		t.Errorf("d survived a cap that sorts before truncating:\n%s", got.Text)
	}
}

// TestSerializeDeterministic: golden on Text. BOTH fixture constraints are
// load-bearing: SQLite GetRelations is UNORDERED (insertion order is the
// pre-sort order — inserted here non-sorted), Postgres orders created_at
// DESC (CreatedAt pinned so that order is non-sorted too). A
// two-runs-byte-equal assertion could not kill drop-sort; the golden does.
func TestSerializeDeterministic(t *testing.T) {
	s := serializeStore(t,
		[]ontology.Entity{
			{ID: "A", Type: "concept", Name: "A"},
			{ID: "B", Type: "concept", Name: "B"},
			{ID: "C", Type: "concept", Name: "C"},
		},
		[]ontology.Relation{
			// insertion order e3, e2, e1 — differs from sorted order.
			// CreatedAt DESC order: e2, e3, e1 — also differs from sorted.
			{ID: "e3", SourceID: "A", TargetID: "B", Relation: "implements",
				CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: "e2", SourceID: "A", TargetID: "C", Relation: "extends",
				CreatedAt: "2026-01-03T00:00:00Z"},
			{ID: "e1", SourceID: "A", TargetID: "B", Relation: "extends",
				CreatedAt: "2026-01-01T00:00:00Z"},
		})
	got, err := SerializeSubgraph(s, []string{"A"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"E1: (A) --[extends]--> (B)",
		"E2: (A) --[extends]--> (C)",
		"E3: (A) --[implements]--> (B)",
	}, "\n")
	if got.Text != want {
		t.Errorf("sorted order broken:\n got:\n%s\nwant:\n%s", got.Text, want)
	}
}

// TestSerializeResolvesAliasSeeds: the probe edge exists ONLY on the
// canonical — after LinkAlias the alias keeps its own edges, so a probe
// edge on the alias would pass even without resolution.
func TestSerializeResolvesAliasSeeds(t *testing.T) {
	s := serializeStore(t,
		[]ontology.Entity{
			{ID: "canon", Type: "concept", Name: "Canon"},
			{ID: "alias", Type: "concept", Name: "Alias"},
			{ID: "x", Type: "concept", Name: "X"},
		},
		[]ontology.Relation{
			{ID: "r1", SourceID: "canon", TargetID: "x", Relation: "extends"},
		})
	if _, err := s.LinkAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := SerializeSubgraph(s, []string{"alias"}, SubgraphOpts{MaxHops: 1, MaxEdges: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !textHasLine(got.Text, "(Canon) --[extends]--> (X)") {
		t.Errorf("alias seed did not land on the canonical's edges:\n%s", got.Text)
	}
	if len(got.Seeds) != 1 || got.Seeds[0] != "canon" {
		t.Errorf("Seeds = %v, want [canon] — post-resolution ids", got.Seeds)
	}
}

// P3-6: the validity window rides the provenance tag when either temporal
// field is set; an open window renders as "now".
func TestProvenanceTagValidityWindow(t *testing.T) {
	f := &fakeOntStore{
		rels: map[string][]store.Relation{
			"a": {
				{SourceID: "a", TargetID: "b", Relation: "extends",
					SourceDoc: "raw/x.md", ValidFrom: "2024-01-01T00:00:00Z"},
				{SourceID: "a", TargetID: "c", Relation: "extends",
					SourceDoc: "raw/x.md", ValidFrom: "2020-01-01T00:00:00Z", ValidTo: "2025-06-01T00:00:00Z"},
				{SourceID: "a", TargetID: "d", Relation: "extends", SourceDoc: "raw/x.md"},
			},
		},
		entities: map[string]*store.Entity{
			"a": {ID: "a", Name: "A"}, "b": {ID: "b", Name: "B"},
			"c": {ID: "c", Name: "C"}, "d": {ID: "d", Name: "D"},
		},
	}
	sg, err := SerializeSubgraph(f, []string{"a"}, SubgraphOpts{MaxHops: 1, MaxEdges: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sg.Text, "valid: 2024-01-01T00:00:00Z→now") {
		t.Errorf("open window must render →now:\n%s", sg.Text)
	}
	if !strings.Contains(sg.Text, "valid: 2020-01-01T00:00:00Z→2025-06-01T00:00:00Z") {
		t.Errorf("closed window must render both ends:\n%s", sg.Text)
	}
	// The no-temporal edge carries no valid: tag at all.
	for _, line := range strings.Split(sg.Text, "\n") {
		if strings.Contains(line, "--[extends]--> (D)") && strings.Contains(line, "valid:") {
			t.Errorf("edge without temporal fields must not render a window: %s", line)
		}
	}
}
