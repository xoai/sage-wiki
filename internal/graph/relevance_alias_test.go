package graph

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// aliasGraphFixture: canon (articled) with its own edge to y (articled);
// alias (articled) with its own edge to n1; alias linked to canon.
func aliasGraphFixture(t *testing.T) *ontology.Store {
	t.Helper()
	s := setupTestStore(t)
	for _, e := range []ontology.Entity{
		{ID: "canon", Type: "concept", Name: "Canon", ArticlePath: "wiki/canon.md"},
		{ID: "alias", Type: "concept", Name: "Alias", ArticlePath: "wiki/alias.md"},
		{ID: "y", Type: "concept", Name: "Y", ArticlePath: "wiki/y.md"},
		{ID: "n1", Type: "concept", Name: "N1", ArticlePath: "wiki/n1.md"},
	} {
		if err := s.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []ontology.Relation{
		{ID: "r1", SourceID: "canon", TargetID: "y", Relation: "extends"},
		{ID: "r2", SourceID: "alias", TargetID: "n1", Relation: "extends"},
	} {
		if err := s.AddRelation(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.LinkAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func scoredIDs(t *testing.T, s *ontology.Store, seeds []string) map[string]*ScoredArticle {
	t.Helper()
	got, err := ScoreRelevance(s, RelevanceOpts{SeedIDs: seeds, MaxExpand: 10, MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*ScoredArticle{}
	for i := range got {
		m[got[i].EntityID] = &got[i]
	}
	return m
}

// Seeding on the ALIAS must land on the canonical's neighbourhood: y is the
// canonical's own edge, which the alias's stored view never reaches.
func TestScoreRelevanceResolvesAliasSeeds(t *testing.T) {
	s := aliasGraphFixture(t)
	m := scoredIDs(t, s, []string{"alias"})
	if m["y"] == nil {
		t.Errorf("seeding on the alias missed the canonical's neighbour y — "+
			"seed resolution is not happening; got %v", keys(m))
	}
}

// Two aliases of one canonical are ONE seed: direct_link must not double.
func TestScoreRelevanceDedupesSeedsResolvingToOneCanonical(t *testing.T) {
	s := aliasGraphFixture(t)
	if err := s.AddEntity(ontology.Entity{
		ID: "alias2", Type: "concept", Name: "Alias2", ArticlePath: "wiki/alias2.md",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkAlias(store.EntityAlias{
		Alias: "alias2", CanonicalID: "canon", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	m := scoredIDs(t, s, []string{"alias", "alias2"})
	if m["y"] == nil {
		t.Fatal("y missing entirely")
	}
	if got := m["y"].Signals["direct_link"]; got != 1.0 {
		t.Errorf("direct_link = %v, want 1.0 — two aliases of one canonical "+
			"double-accumulated as two seeds", got)
	}
}

// The raw alias id stays in seedSet: neither half of the cluster is
// re-suggested as its own expansion.
func TestScoreRelevanceAliasNotSuggestedAsExpansion(t *testing.T) {
	s := aliasGraphFixture(t)
	// An intra-cluster edge makes the alias a neighbour of the canonical.
	if err := s.AddRelation(ontology.Relation{
		ID: "r3", SourceID: "canon", TargetID: "alias", Relation: "extends",
	}); err != nil {
		t.Fatal(err)
	}
	m := scoredIDs(t, s, []string{"alias"})
	if m["alias"] != nil {
		t.Errorf("the seed's own alias half was suggested as an expansion of itself")
	}
	if m["canon"] != nil {
		t.Errorf("the canonical was suggested as an expansion of its own alias seed")
	}
}

func keys(m map[string]*ScoredArticle) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
