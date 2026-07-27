package ontology

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// C1 — a store that has only ever seen an empty derived table must still learn
// about rows another store (or another process) wrote. Before the rate-limited
// re-probe this returned 0 forever, so `serve` omitted every alias-derived edge
// until restart.
func TestDerivedGuardSeesWritesFromAnotherStore(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "c1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mk := func() *Store {
		return NewStore(db, ValidRelationNames(BuiltinRelations), ValidEntityTypeNames(BuiltinEntityTypes))
	}
	writer, reader := mk(), mk()
	for _, id := range []string{"C", "A", "X"} {
		if err := writer.AddEntity(Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.AddRelation(Relation{ID: "r1", SourceID: "A", TargetID: "X", Relation: RelExtends}); err != nil {
		t.Fatal(err)
	}

	// Warm the reader's guard while the table is still empty — the exact state
	// that used to be permanent.
	if _, err := reader.GetRelations("C", Both, ""); err != nil {
		t.Fatal(err)
	}

	if _, err := writer.LinkAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "C", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// The reader re-probes on the false path, so it must now see the derived edge.
	// Backdate, do NOT zero: zero is "never probed", which the buggy once-only
	// implementation also re-probes from — a test built on that passes against
	// the bug.
	reader.ageDerivedProbe()
	got, err := reader.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("a second store sees %d edges after another store linked; want 1 — "+
			"a stale-false guard hides derived edges", len(got))
	}
}
