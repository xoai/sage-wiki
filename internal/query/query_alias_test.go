package query

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// seedAliasNeighborhood wires the ontology so that the search hit is an ALIAS
// whose canonical — not the alias itself — owns the edge to an articled
// neighbor. The related-articles fallback only surfaces neighbor.md if it
// resolves the alias before traversing; the alias's own stored view has no
// edges at all.
func seedAliasNeighborhood(t *testing.T, dir string, db *storage.DB) string {
	t.Helper()
	neighborRel := filepath.Join("wiki", "concepts", "neighbor.md")
	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0755)
	os.WriteFile(filepath.Join(dir, neighborRel),
		[]byte("# Neighbor\n\nNeighbor body reachable only through the canonical entity."), 0644)

	ont := ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.BuiltinRelations),
		ontology.ValidEntityTypeNames(ontology.BuiltinEntityTypes))
	for _, e := range []ontology.Entity{
		{ID: "alias", Type: "concept", Name: "Alias"},
		{ID: "canon", Type: "concept", Name: "Canon"},
		{ID: "neighbor", Type: "concept", Name: "Neighbor", ArticlePath: neighborRel},
	} {
		if err := ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := ont.AddRelation(ontology.Relation{
		ID: "r1", SourceID: "canon", TargetID: "neighbor", Relation: "extends",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ont.LinkAlias(store.EntityAlias{
		Alias: "alias", CanonicalID: "canon", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	return neighborRel
}

// TestQueryGraphExpansionResolvesAlias pins the expansion-ON seam end-to-end:
// search hit → seed extraction → computeGraphExpansion → ScoreRelevance. The
// seed-resolution behavior lives in ScoreRelevance (unit-pinned in
// internal/graph), but nothing below asserts the composition — this does. With
// expansion on (the default), an alias hit must surface the CANONICAL's
// neighbor as a Graph-related article.
func TestQueryGraphExpansionResolvesAlias(t *testing.T) {
	dir, cfg, db := preambleHarness(t)
	neighborRel := seedAliasNeighborhood(t, dir, db)

	aliasRel := filepath.Join("wiki", "concepts", "alias.md")
	os.WriteFile(filepath.Join(dir, aliasRel),
		[]byte("# Alias\n\nAlias article about quantum flux capacitors."), 0644)
	mem := memory.NewStore(db)
	if err := mem.Add(memory.Entry{
		ID: "concept:alias", Content: "quantum flux capacitor alias concept",
		ArticlePath: aliasRel,
	}); err != nil {
		t.Fatalf("mem.Add: %v", err)
	}

	ctx, _, _, err := buildQueryContext(context.Background(), dir, "quantum flux", 5, cfg, db)
	if err != nil {
		t.Fatalf("buildQueryContext: %v", err)
	}
	if !strings.Contains(ctx, "### Graph-related: "+neighborRel) {
		t.Errorf("expansion-on path missed the canonical's neighbor — alias seed not resolved through ScoreRelevance:\n%q", ctx)
	}
}

// TestQueryRelatedFallbackResolvesAlias covers both fallback traversal sites in
// query.go — buildContextFromEnhanced and buildDocLevelContext each have their
// own copy of the loop, so each subtest drives exactly one of them (the same
// way the preamble tests split at chunkCount) and a fix at one site cannot
// mask a regression at the other.
//
// Graph expansion is DISABLED in both subtests: ScoreRelevance resolves alias
// seeds itself, so with expansion on it surfaces the neighbor as
// "### Graph-related:" and marks it seen — masking the fallback entirely. The
// fallback loop is the path users are on when graph_expansion is off, and it
// must resolve on its own.
func TestQueryRelatedFallbackResolvesAlias(t *testing.T) {
	expansionOff := false

	t.Run("doc-level", func(t *testing.T) {
		dir, cfg, db := preambleHarness(t)
		cfg.Search.GraphExpansion = &expansionOff
		neighborRel := seedAliasNeighborhood(t, dir, db)

		aliasRel := filepath.Join("wiki", "concepts", "alias.md")
		os.WriteFile(filepath.Join(dir, aliasRel),
			[]byte("# Alias\n\nAlias article about quantum flux capacitors."), 0644)
		mem := memory.NewStore(db)
		if err := mem.Add(memory.Entry{
			ID: "concept:alias", Content: "quantum flux capacitor alias concept",
			ArticlePath: aliasRel,
		}); err != nil {
			t.Fatalf("mem.Add: %v", err)
		}

		ctx, _, _, err := buildQueryContext(context.Background(), dir, "quantum flux", 5, cfg, db)
		if err != nil {
			t.Fatalf("buildQueryContext: %v", err)
		}
		if !strings.Contains(ctx, "### Related: "+neighborRel) {
			t.Errorf("doc-level fallback missed the canonical's neighbor — alias seed not resolved:\n%q", ctx)
		}
	})

	t.Run("enhanced", func(t *testing.T) {
		dir, cfg, db := preambleHarness(t)
		cfg.Search.GraphExpansion = &expansionOff
		neighborRel := seedAliasNeighborhood(t, dir, db)

		aliasRel := filepath.Join("wiki", "concepts", "alias.md")
		os.WriteFile(filepath.Join(dir, aliasRel),
			[]byte("# Alias\n\nAlias article about quantum flux capacitors."), 0644)
		chunks := memory.NewChunkStore(db)
		if err := db.WriteTx(func(tx *sql.Tx) error {
			return chunks.IndexChunks(tx, "concept:alias", []memory.ChunkEntry{{
				ChunkID: "concept:alias:0", ChunkIndex: 0, Heading: "Alias",
				Content:     "quantum flux capacitor alias concept",
				StartOffset: 0, EndOffset: 40,
			}})
		}); err != nil {
			t.Fatalf("IndexChunks: %v", err)
		}

		ctx, _, _, err := buildQueryContext(context.Background(), dir, "quantum flux", 5, cfg, db)
		if err != nil {
			t.Fatalf("buildQueryContext: %v", err)
		}
		if !strings.Contains(ctx, "### Related: "+neighborRel) {
			t.Errorf("enhanced fallback missed the canonical's neighbor — alias seed not resolved:\n%q", ctx)
		}
	})
}
