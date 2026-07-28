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
)

// seedProvenanceNeighborhood: the search hit "seedent" has one OUTBOUND
// articled neighbor (seedent→outn, doc raw/o.md) and one INBOUND articled
// neighbor (inn→seedent, doc raw/i.md). The inbound row is load-bearing: a
// via-map keyed on literal TargetID keys inbound edges on the seed itself,
// so their provenance silently vanishes behind the defensive omit.
func seedProvenanceNeighborhood(t *testing.T, dir string, db *storage.DB) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0755)
	for _, f := range []struct{ rel, body string }{
		{filepath.Join("wiki", "concepts", "outn.md"), "# Outn\n\nOutbound neighbor body."},
		{filepath.Join("wiki", "concepts", "inn.md"), "# Inn\n\nInbound neighbor body."},
	} {
		os.WriteFile(filepath.Join(dir, f.rel), []byte(f.body), 0644)
	}

	ont := ontology.NewStore(db,
		ontology.ValidRelationNames(ontology.BuiltinRelations),
		ontology.ValidEntityTypeNames(ontology.BuiltinEntityTypes))
	for _, e := range []ontology.Entity{
		{ID: "seedent", Type: "concept", Name: "Seedent"},
		{ID: "outn", Type: "concept", Name: "Outn", ArticlePath: filepath.Join("wiki", "concepts", "outn.md")},
		{ID: "inn", Type: "concept", Name: "Inn", ArticlePath: filepath.Join("wiki", "concepts", "inn.md")},
	} {
		if err := ont.AddEntity(e); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []ontology.Relation{
		{ID: "ro", SourceID: "seedent", TargetID: "outn", Relation: "extends",
			SourceDoc: "raw/o.md", Confidence: 0.8},
		{ID: "ri", SourceID: "inn", TargetID: "seedent", Relation: "extends",
			SourceDoc: "raw/i.md", Confidence: 0.7},
	} {
		if err := ont.AddRelation(r); err != nil {
			t.Fatal(err)
		}
	}
}

func assertViaLines(t *testing.T, ctx string) {
	t.Helper()
	if !strings.Contains(ctx, "via: (seedent) --[extends]--> (outn) {source: raw/o.md, confidence: 0.80}") {
		t.Errorf("outbound via-line missing:\n%q", ctx)
	}
	if !strings.Contains(ctx, "via: (inn) --[extends]--> (seedent) {source: raw/i.md, confidence: 0.70}") {
		t.Errorf("inbound via-line missing — the neighbor-keyed map is what makes this work:\n%q", ctx)
	}
}

// TestQueryFallbackCarriesEdgeProvenance: each fallback site annotates its
// "### Related:" blocks with the connecting edge's provenance. Expansion is
// disabled (the TestQueryRelatedFallbackResolvesAlias rationale — expansion
// masks the fallback); one subtest per site, an inbound row in each.
func TestQueryFallbackCarriesEdgeProvenance(t *testing.T) {
	expansionOff := false

	t.Run("doc-level", func(t *testing.T) {
		dir, cfg, db := preambleHarness(t)
		cfg.Search.GraphExpansion = &expansionOff
		seedProvenanceNeighborhood(t, dir, db)
		mem := memory.NewStore(db)
		if err := mem.Add(memory.Entry{
			ID: "concept:seedent", Content: "quantum flux capacitor seed concept",
		}); err != nil {
			t.Fatal(err)
		}
		ctx, _, _, err := buildQueryContext(context.Background(), dir, "quantum flux", 5, cfg, db)
		if err != nil {
			t.Fatal(err)
		}
		assertViaLines(t, ctx)
	})

	t.Run("enhanced", func(t *testing.T) {
		dir, cfg, db := preambleHarness(t)
		cfg.Search.GraphExpansion = &expansionOff
		seedProvenanceNeighborhood(t, dir, db)
		chunks := memory.NewChunkStore(db)
		if err := db.WriteTx(func(tx *sql.Tx) error {
			return chunks.IndexChunks(tx, "concept:seedent", []memory.ChunkEntry{{
				ChunkID: "concept:seedent:0", ChunkIndex: 0, Heading: "Seedent",
				Content:     "quantum flux capacitor seed concept",
				StartOffset: 0, EndOffset: 40,
			}})
		}); err != nil {
			t.Fatal(err)
		}
		ctx, _, _, err := buildQueryContext(context.Background(), dir, "quantum flux", 5, cfg, db)
		if err != nil {
			t.Fatal(err)
		}
		assertViaLines(t, ctx)
	})
}
