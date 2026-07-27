package ontology

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

func derivedStore(t *testing.T) (*Store, *storage.DB) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := NewStore(db, ValidRelationNames(BuiltinRelations), ValidEntityTypeNames(BuiltinEntityTypes))
	for _, id := range []string{"C", "A", "X", "Y", "Q"} {
		if err := s.AddEntity(Entity{ID: id, Type: "concept", Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	return s, db
}

func addDerived(t *testing.T, db *storage.DB, alias, id, src, tgt, evidence string) {
	t.Helper()
	if _, err := db.WriteDB().Exec(
		`INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation,evidence)
		 VALUES (?,?,?,?,?,?)`, alias, id, src, tgt, RelExtends, evidence); err != nil {
		t.Fatalf("insert derived: %v", err)
	}
}

// With no derived rows the emitted SQL must be the pre-change statement, and the
// results identical. This is what makes M2 safe to merge on its own: the union
// exists but is dormant.
func TestDerivedGuardDormantWhenEmpty(t *testing.T) {
	s, _ := derivedStore(t)
	if err := s.AddRelation(Relation{ID: "r1", SourceID: "C", TargetID: "X", Relation: RelExtends}); err != nil {
		t.Fatal(err)
	}

	if s.derivedExists() {
		t.Fatal("derivedExists() = true with an empty derived_relations")
	}
	if q := s.unionIfDerived("SELECT 1 FROM relations", "1=1"); q != "SELECT 1 FROM relations" {
		t.Errorf("guard false must emit the pre-change statement, got:\n%s", q)
	}

	rels, err := s.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].ID != "r1" {
		t.Errorf("GetRelations = %+v, want just r1", rels)
	}
}

// A derived edge is returned alongside originals once the table is populated.
func TestDerivedEdgeIsReturned(t *testing.T) {
	s, db := derivedStore(t)
	if err := s.AddRelation(Relation{ID: "r1", SourceID: "C", TargetID: "X", Relation: RelExtends}); err != nil {
		t.Fatal(err)
	}
	addDerived(t, db, "A", "alias:1111111111111111", "C", "Y", "derived")
	s.markDerivedWritten()

	rels, err := s.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rels {
		got[r.TargetID] = true
	}
	if len(rels) != 2 || !got["X"] || !got["Y"] {
		t.Errorf("GetRelations = %+v, want C->X (original) and C->Y (derived)", rels)
	}
}

// Precedence: a derived row must never displace an edge the canonical asserted
// itself, or the canonical's own evidence becomes unrecoverable. This is what
// LinkAlias's ON CONFLICT DO NOTHING gives today.
func TestDerivedNeverShadowsAnOriginal(t *testing.T) {
	s, db := derivedStore(t)
	if err := s.AddRelation(Relation{
		ID: "r1", SourceID: "C", TargetID: "X", Relation: RelExtends, Evidence: "CANONICAL",
	}); err != nil {
		t.Fatal(err)
	}
	addDerived(t, db, "A", "alias:2222222222222222", "C", "X", "DERIVED")
	s.markDerivedWritten()

	rels, err := s.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("rows = %d, want 1 — the derived row must be suppressed: %+v", len(rels), rels)
	}
	if rels[0].Evidence != "CANONICAL" {
		t.Errorf("evidence = %q, want CANONICAL — the original must win", rels[0].Evidence)
	}
}

// Precedence must hold when the canonical's id is NULL, which relations permits
// and relationCols COALESCEs. Keying the anti-join on r.id would miss this.
func TestDerivedNeverShadowsANullIDOriginal(t *testing.T) {
	s, db := derivedStore(t)
	if _, err := db.WriteDB().Exec(
		`INSERT INTO relations (id,source_id,target_id,relation,evidence) VALUES (NULL,'C','X',?,'CANONICAL')`,
		RelExtends); err != nil {
		t.Fatal(err)
	}
	addDerived(t, db, "A", "alias:3333333333333333", "C", "X", "DERIVED")
	s.markDerivedWritten()

	rels, err := s.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Evidence != "CANONICAL" {
		t.Errorf("rows = %+v, want the NULL-id original to still suppress the derived row", rels)
	}
}

// Two aliases may derive one edge — that is legal, and un-linking one must leave
// the other's row. A read returns it once, lowest alias_id winning. DISTINCT
// cannot do this: each row carries its own alias's evidence.
func TestTwoAliasesDerivingOneEdgeReturnOneRow(t *testing.T) {
	s, db := derivedStore(t)
	addDerived(t, db, "al2", "alias:4444444444444444", "C", "Q", "FROM-AL2")
	addDerived(t, db, "al1", "alias:4444444444444444", "C", "Q", "FROM-AL1")
	s.markDerivedWritten()

	rels, err := s.GetRelations("C", Both, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rels), rels)
	}
	if rels[0].Evidence != "FROM-AL1" {
		t.Errorf("evidence = %q, want FROM-AL1 (lowest alias_id wins)", rels[0].Evidence)
	}
}

// Direction and relation-type filters must apply to BOTH arms, or a filtered
// query returns derived edges it excluded from the originals.
func TestDerivedRespectsDirectionAndType(t *testing.T) {
	s, db := derivedStore(t)
	addDerived(t, db, "A", "alias:5555555555555555", "C", "Y", "out")
	addDerived(t, db, "A", "alias:6666666666666666", "X", "C", "in")
	s.markDerivedWritten()

	out, err := s.GetRelations("C", Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].TargetID != "Y" {
		t.Errorf("Outbound = %+v, want only C->Y", out)
	}

	in, err := s.GetRelations("C", Inbound, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].SourceID != "X" {
		t.Errorf("Inbound = %+v, want only X->C", in)
	}

	none, err := s.GetRelations("C", Both, RelCites)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("type filter leaked derived rows: %+v", none)
	}
}

// The guard's only path back to false re-reads; it is never optimistically
// cleared, because a stale false hides edges.
func TestDerivedGuardRechecksRatherThanAssuming(t *testing.T) {
	s, db := derivedStore(t)
	addDerived(t, db, "A", "alias:7777777777777777", "C", "Y", "x")
	s.markDerivedWritten()
	if !s.derivedExists() {
		t.Fatal("guard should be true after a derived write")
	}

	// A delete that does NOT empty the table must leave the guard true.
	addDerived(t, db, "B", "alias:8888888888888888", "C", "X", "y")
	if _, err := db.WriteDB().Exec(`DELETE FROM derived_relations WHERE alias_id='A'`); err != nil {
		t.Fatal(err)
	}
	s.markDerivedMaybeEmpty()
	if !s.derivedExists() {
		t.Error("guard went false while a derived row remains — that would hide the edge")
	}

	if _, err := db.WriteDB().Exec(`DELETE FROM derived_relations`); err != nil {
		t.Fatal(err)
	}
	s.markDerivedMaybeEmpty()
	if s.derivedExists() {
		t.Error("guard stayed true after the table emptied")
	}
}

// T3.3 — the upgrade path. A vault written before decision-035 has anonymous
// copies sitting in `relations`, indistinguishable from originals. LinkAlias
// converts them in place: the copy is identified by EXACT id (copiedRelationID
// for its own endpoints), deleted, and re-inserted with its cause recorded.
//
// The decoy matters as much as the copy. Two earlier design revisions specified
// a `LIKE 'alias:%'` predicate, which would have destroyed a real edge whose
// source entity id merely starts with "alias:" — reachable from the public MCP
// tool, which composes relation ids from caller-supplied arguments.
func TestLinkAliasConvertsAnonymousCopiesInPlace(t *testing.T) {
	s, db := derivedStore(t)
	if err := s.AddEntity(Entity{ID: "alias:x", Type: "concept", Name: "decoy"}); err != nil {
		t.Fatal(err)
	}
	// The alias owns a real edge.
	if err := s.AddRelation(Relation{
		ID: "r1", SourceID: "A", TargetID: "X", Relation: RelExtends, Evidence: "ORIGINAL",
	}); err != nil {
		t.Fatal(err)
	}
	// P3-3 would have copied it onto the canonical, anonymously.
	anon := copiedRelationID("C", RelExtends, "X")
	if _, err := db.WriteDB().Exec(
		`INSERT INTO relations (id,source_id,target_id,relation,evidence) VALUES (?,?,?,?,?)`,
		anon, "C", "X", RelExtends, "ANON-COPY"); err != nil {
		t.Fatal(err)
	}
	// A decoy a LIKE predicate would have matched: source entity id "alias:x".
	if _, err := db.WriteDB().Exec(
		`INSERT INTO relations (id,source_id,target_id,relation) VALUES (?,?,?,?)`,
		"alias:x-extends-X", "alias:x", "X", RelExtends); err != nil {
		t.Fatal(err)
	}

	res, err := s.LinkAlias(store.EntityAlias{
		Alias: "A", CanonicalID: "C", EntityType: "concept",
		Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Converted != 1 {
		t.Errorf("Converted = %d, want 1 — the anonymous copy should have moved", res.Converted)
	}

	// The anonymous copy is gone from relations...
	var n int
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM relations WHERE id=?`, anon).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the anonymous copy survived in relations — it is still unattributable")
	}
	// ...and now exists as a derived row, attributed to A.
	var alias string
	if err := db.ReadDB().QueryRow(
		`SELECT alias_id FROM derived_relations WHERE source_id='C' AND target_id='X'`).Scan(&alias); err != nil {
		t.Fatalf("the copy was not re-inserted as a derived row: %v", err)
	}
	if alias != "A" {
		t.Errorf("derived row stamped %q, want A", alias)
	}

	// The decoy and the original are untouched.
	if err := db.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM relations WHERE id='alias:x-extends-X'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the decoy was deleted — an exact-id match must not behave like a LIKE predicate")
	}
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM relations WHERE id='r1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("the alias's own original edge was deleted")
	}
}

// ListRelations orders by ORDINAL 5 across the union, because relationCols
// selects a COALESCE expression rather than the bare column, and SQLite cannot
// resolve that name in an
// ORDER BY over a compound SELECT. An ordinal is only safe while the column
// order holds, and relationCols' own comment invites editors to change it in
// lockstep with scanRelations — which would silently sort by the wrong column.
//
// This pins the ordinal to the column it means.
func TestRelationColsOrdinalFiveIsCreatedAt(t *testing.T) {
	_, db := derivedStore(t)
	if _, err := db.WriteDB().Exec(
		`INSERT INTO relations (id,source_id,target_id,relation,created_at,evidence)
		 VALUES ('r1','C','X',?,'2020-01-02T00:00:00Z','EV')`, RelExtends); err != nil {
		t.Fatal(err)
	}
	var fifth string
	if err := db.ReadDB().QueryRow(
		`SELECT `+relationCols+` FROM relations WHERE id='r1'`).Scan(
		new(string), new(string), new(string), new(string), &fifth,
		new(string), new(float64), new(string), new(string), new(string), new(string),
	); err != nil {
		t.Fatal(err)
	}
	if fifth != "2020-01-02T00:00:00Z" {
		t.Errorf("relationCols column 5 = %q, want created_at — ListRelations sorts by "+
			"ORDER BY 5 across the union and would silently sort by the wrong column", fifth)
	}
}

// ...and the ordering it produces must actually be honoured across the union.
func TestListRelationsOrdersAcrossTheUnion(t *testing.T) {
	s, db := derivedStore(t)
	if _, err := db.WriteDB().Exec(
		`INSERT INTO relations (id,source_id,target_id,relation,created_at)
		 VALUES ('old','C','X',?,'2020-01-01T00:00:00Z'),('new','C','Y',?,'2026-01-01T00:00:00Z')`,
		RelExtends, RelExtends); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteDB().Exec(
		`INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation,created_at)
		 VALUES ('A','alias:aaaaaaaaaaaaaaaa','C','Q',?,'2023-01-01T00:00:00Z')`,
		RelExtends); err != nil {
		t.Fatal(err)
	}
	s.markDerivedWritten()

	got, err := s.ListRelations("", -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3 (2 original + 1 derived)", len(got))
	}
	want := []string{"new", "alias:aaaaaaaaaaaaaaaa", "old"} // created_at DESC
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("position %d = %q, want %q — the derived row must sort into the "+
				"whole result, not after it", i, got[i].ID, id)
		}
	}
}

// ageDerivedProbe backdates the last probe so the next derivedExists re-probes,
// WITHOUT resetting probedAt to zero.
//
// The distinction is the whole test. Zero means "never probed", and the buggy
// once-only implementation re-probes from that state too — so a test built on it
// passes against the bug it exists to catch. Backdating models a guard that HAS
// probed, found nothing, and must now notice someone else's write. Test-only;
// the rate limit is wall-clock and a test must not sleep to observe it.
func (s *Store) ageDerivedProbe() {
	s.derivedMu.Lock()
	s.probedAt = time.Now().Add(-2 * derivedRecheck)
	s.derivedMu.Unlock()
}
