package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// The Postgres twin of internal/ontology/derived_test.go. Cross-backend
// conformance for these semantics lands in M3, when LinkAlias gives storetest an
// interface-level writer for derived_relations; until then each backend proves
// its own union with raw SQL.
func derivedTestBackend(t *testing.T) (*backend, *sql.DB, func()) {
	t.Helper()
	dsn := migrationTestDSN(t)
	name := fmt.Sprintf("derived_%d", time.Now().UnixNano())
	boot, err := sql.Open("pgx", swapDB(dsn, "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	createClone(t, boot, name, dsnDB(dsn))
	boot.Close()
	testDSN := swapDB(dsn, name)

	b, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	raw, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"C", "A", "X", "Y", "Q"} {
		if _, err := raw.Exec(`INSERT INTO entities (id,type,name) VALUES ($1,'concept',$1)
		                       ON CONFLICT DO NOTHING`, id); err != nil {
			t.Fatal(err)
		}
	}
	cleanup := func() {
		raw.Close()
		b.Close()
		c, err := sql.Open("pgx", swapDB(dsn, "postgres"))
		if err == nil {
			c.Exec("DROP DATABASE " + name)
			c.Close()
		}
	}
	return b.(*backend), raw, cleanup
}

func addDerivedPG(t *testing.T, raw *sql.DB, alias, id, src, tgt, evidence string) {
	t.Helper()
	if _, err := raw.Exec(
		`INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation,evidence)
		 VALUES ($1,$2,$3,$4,'extends',$5)`, alias, id, src, tgt, evidence); err != nil {
		t.Fatalf("insert derived: %v", err)
	}
}

func TestPGDerivedUnion(t *testing.T) {
	b, raw, cleanup := derivedTestBackend(t)
	defer cleanup()
	ont := b.Ontology()

	if b.derivedExists() {
		t.Fatal("derivedExists() = true with an empty derived_relations")
	}
	if err := ont.AddRelation(store.Relation{
		ID: "r1", SourceID: "C", TargetID: "X", Relation: "extends", Evidence: "CANONICAL",
	}); err != nil {
		t.Fatal(err)
	}

	// Dormant: only the original.
	rels, err := ont.GetRelations("C", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].ID != "r1" {
		t.Fatalf("guard false must return only originals, got %+v", rels)
	}

	// A derived edge appears once the table is populated.
	addDerivedPG(t, raw, "A", "alias:1111111111111111", "C", "Y", "derived")
	b.markDerivedWritten()
	rels, err = ont.GetRelations("C", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 {
		t.Errorf("rows = %d, want 2 (original + derived): %+v", len(rels), rels)
	}

	// Precedence: a derived row must never displace the canonical's own edge.
	addDerivedPG(t, raw, "A", "alias:2222222222222222", "C", "X", "DERIVED")
	rels, err = ont.GetRelations("C", store.Both, "extends")
	if err != nil {
		t.Fatal(err)
	}
	var cx *store.Relation
	for i := range rels {
		if rels[i].TargetID == "X" {
			cx = &rels[i]
		}
	}
	if cx == nil {
		t.Fatal("C->X missing entirely")
	}
	if cx.Evidence != "CANONICAL" {
		t.Errorf("C->X evidence = %q, want CANONICAL — the original must win", cx.Evidence)
	}

	// Dedup: two aliases deriving one edge return one row, lowest alias_id.
	addDerivedPG(t, raw, "al2", "alias:4444444444444444", "C", "Q", "FROM-AL2")
	addDerivedPG(t, raw, "al1", "alias:4444444444444444", "C", "Q", "FROM-AL1")
	rels, err = ont.GetRelations("C", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	n, ev := 0, ""
	for _, r := range rels {
		if r.TargetID == "Q" {
			n++
			ev = r.Evidence
		}
	}
	if n != 1 {
		t.Errorf("C->Q rows = %d, want 1", n)
	}
	if ev != "FROM-AL1" {
		t.Errorf("C->Q evidence = %q, want FROM-AL1 (lowest alias_id)", ev)
	}

	// Degree counts the union, deduped.
	deg, err := ont.EntityDegree("C")
	if err != nil {
		t.Fatal(err)
	}
	if deg != 3 { // C->X (original), C->Y (derived), C->Q (one of two)
		t.Errorf("EntityDegree(C) = %d, want 3", deg)
	}

	// Direction filters must apply to both arms.
	out, err := ont.GetRelations("C", store.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Errorf("Outbound = %d, want 3: %+v", len(out), out)
	}
	in, err := ont.GetRelations("C", store.Inbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 0 {
		t.Errorf("Inbound = %d, want 0: %+v", len(in), in)
	}

	// The guard's only path back to false re-reads rather than assuming: a
	// stale false would hide derived edges, so a partial delete must leave it
	// true. Same contract as the SQLite twin.
	if _, err := raw.Exec(`DELETE FROM derived_relations WHERE alias_id='al1'`); err != nil {
		t.Fatal(err)
	}
	b.markDerivedMaybeEmpty()
	if !b.derivedExists() {
		t.Error("guard went false while derived rows remain — that would hide edges")
	}
	if _, err := raw.Exec(`DELETE FROM derived_relations`); err != nil {
		t.Fatal(err)
	}
	b.markDerivedMaybeEmpty()
	if b.derivedExists() {
		t.Error("guard stayed true after the table emptied")
	}
}

// C1, Postgres. Arguably MORE reachable here than on SQLite: `serve` runs as a
// reader alongside a compiling writer, and Postgres's advisory writer lock makes
// that the only concurrent shape. A guard that probed once and found nothing
// would omit every derived edge for the reader's lifetime.
func TestPGDerivedGuardSeesWritesFromAnotherBackend(t *testing.T) {
	b, raw, cleanup := derivedTestBackend(t)
	defer cleanup()

	// Warm the guard FALSE while the table is genuinely empty.
	if b.derivedExists() {
		t.Fatal("setup: guard should start false")
	}
	if _, err := b.Ontology().GetRelations("C", store.Both, ""); err != nil {
		t.Fatal(err)
	}

	// Another writer lands a derived row; this backend is not told.
	addDerivedPG(t, raw, "A", "alias:9999999999999999", "C", "X", "from elsewhere")

	// Backdate rather than zero — zero is "never probed", which the buggy
	// once-only implementation also re-probes from.
	b.ageDerivedProbe()
	rels, err := b.Ontology().GetRelations("C", store.Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Errorf("backend sees %d edges after another writer derived one; want 1 — "+
			"a stale-false guard hides derived edges until restart", len(rels))
	}
}

// ageDerivedProbe backdates the last probe so the next derivedExists re-probes
// without resetting probedAt to zero — see the SQLite twin for why the
// distinction is the whole test. Test-only.
func (b *backend) ageDerivedProbe() {
	b.derivedMu.Lock()
	b.probedAt = time.Now().Add(-2 * derivedRecheck)
	b.derivedMu.Unlock()
}
