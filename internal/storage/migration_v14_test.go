package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// V13→V14 upgrade: a database AT the prior schema version (fixture built
// from the real V1..V13 consts, with data) migrates cleanly, keeps its
// data, and gains the community tables (P3-5). Precedent: migration_v13_test.go.
func TestMigrationV14Upgrade(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{
		migrationV1, migrationV2, migrationV3, migrationV4, migrationV5,
		migrationV6, migrationV7, migrationV8, migrationV9, migrationV10,
		migrationV11, migrationV12, migrationV13,
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
	}
	if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	// Data at V13 that must survive the upgrade.
	if _, err := raw.Exec(`INSERT INTO entities (id, type, name) VALUES ('e1', 'concept', 'One')`); err != nil {
		t.Fatalf("seed entity: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO entities (id, type, name) VALUES ('e2', 'concept', 'Two')`); err != nil {
		t.Fatalf("seed entity 2: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO relations (id, source_id, target_id, relation) VALUES ('r1', 'e1', 'e2', 'extends')`); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
	raw.Close()

	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.ReadDB().QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil || version != 15 {
		t.Fatalf("schema version = %d, want 15 (err %v)", version, err)
	}
	for _, table := range []string{"communities", "community_members"} {
		var name string
		err := db.ReadDB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil || name != table {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
	var n int
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&n); err != nil || n != 2 {
		t.Errorf("V13 entities lost across migration: n=%d err=%v", n, err)
	}
	if err := db.ReadDB().QueryRow(`SELECT COUNT(*) FROM relations`).Scan(&n); err != nil || n != 1 {
		t.Errorf("V13 relations lost across migration: n=%d err=%v", n, err)
	}
}
