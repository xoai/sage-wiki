package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildV11Fixture creates a V11 DB from the real migration consts — never
// hand-copied schema SQL, which drifts from the runner silently — then opens it
// through Open so the pending V12 migration actually runs.
func buildV11Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v11.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{
		migrationV1, migrationV2, migrationV3, migrationV4, migrationV5,
		migrationV6, migrationV7, migrationV8, migrationV9, migrationV10,
		migrationV11,
	}
	for i, sqlText := range consts {
		if i == 3 { // V4 rebuilds entities; the runner disables FKs for it.
			if _, err := raw.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(sqlText); err != nil {
			t.Fatalf("fixture migration v%d: %v", i+1, err)
		}
		if _, err := raw.Exec("INSERT INTO schema_version (version) VALUES (?)", i+1); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open (migrates to current): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMigrationV12CreatesDerivedRelations covers the schema half of the
// reversible-alias-links work (decision-035): derived_relations holds the copies
// LinkAlias used to write into `relations`, each stamped with the alias that
// caused it, so un-link is a delete by cause rather than a reconstruction.
//
// M1 only creates the table. Nothing reads or writes it until M2/M3, so every
// existing test must still pass unchanged — that equivalence is the milestone.
func TestMigrationV12CreatesDerivedRelations(t *testing.T) {
	db := buildV11Fixture(t)

	// The 11 relation columns plus alias_id. `id` is NOT optional: the Postgres
	// read path scans a bare `id` into a string, so a NULL there would fail every
	// read that returns a derived row.
	for _, col := range []string{
		"alias_id", "id", "source_id", "target_id", "relation", "created_at",
		"evidence", "confidence", "source_doc", "valid_from", "valid_to",
		"invalidated_by",
	} {
		var n int
		if err := db.ReadDB().QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('derived_relations') WHERE name=?`,
			col).Scan(&n); err != nil || n != 1 {
			t.Errorf("derived_relations.%s missing after V12 (n=%d, err=%v)", col, n, err)
		}
	}

	for _, idx := range []string{
		"idx_derived_source", "idx_derived_target", "idx_derived_alias",
	} {
		var n int
		if err := db.ReadDB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`,
			idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after V12 (n=%d, err=%v)", idx, n, err)
		}
	}

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != CurrentSchemaVersion() {
		t.Errorf("schema version = %d, want %d", version, CurrentSchemaVersion())
	}
}

// A pruned entity must take its derived edges with it. `relations` already
// cascades (db.go), and derived rows are edges too — leaving them behind would
// surface edges pointing at a row that no longer exists.
func TestMigrationV12DerivedCascadesOnEntityDelete(t *testing.T) {
	db := buildV11Fixture(t)

	if _, err := db.WriteDB().Exec(
		`INSERT INTO entities (id,type,name) VALUES ('C','concept','C'),('X','concept','X')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteDB().Exec(
		`INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation)
		 VALUES ('A','alias:deadbeef0000','C','X','extends')`); err != nil {
		t.Fatalf("insert derived row: %v", err)
	}

	if _, err := db.WriteDB().Exec(`DELETE FROM entities WHERE id='X'`); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM derived_relations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("derived rows after deleting the target entity = %d, want 0 (ON DELETE CASCADE)", n)
	}
}

// Two aliases may derive the same edge — that is legal and load-bearing, because
// un-linking one must leave the other's row. The primary key has to permit it.
func TestMigrationV12PermitsTwoAliasesPerEdge(t *testing.T) {
	db := buildV11Fixture(t)

	if _, err := db.WriteDB().Exec(
		`INSERT INTO entities (id,type,name) VALUES ('C','concept','C'),('Q','concept','Q')`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation,evidence)
	        VALUES (?,?,'C','Q','extends',?)`
	if _, err := db.WriteDB().Exec(ins, "al1", "alias:1111111111111111", "from al1"); err != nil {
		t.Fatalf("first alias: %v", err)
	}
	if _, err := db.WriteDB().Exec(ins, "al2", "alias:1111111111111111", "from al2"); err != nil {
		t.Fatalf("second alias deriving the same edge must be permitted: %v", err)
	}

	// ...but the same alias asserting it twice is a no-op, not a duplicate.
	if _, err := db.WriteDB().Exec(
		`INSERT OR IGNORE INTO derived_relations (alias_id,id,source_id,target_id,relation)
		 VALUES ('al1','alias:1111111111111111','C','Q','extends')`); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := db.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM derived_relations WHERE source_id='C' AND target_id='Q'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("derived rows = %d, want 2 (one per alias, idempotent per alias)", n)
	}
}
