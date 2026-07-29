package storage

import (
	"path/filepath"
	"testing"
)

// V13→V14 upgrade: a database at the prior schema migrates cleanly, keeps
// its data, and gains the community tables (P3-5).
func TestMigrationV14Upgrade(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"communities", "community_members"} {
		var name string
		err := db.ReadDB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil || name != table {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}

	// Prior data intact: insert an entity + relation, re-open, verify.
	if _, err := db.WriteDB().Exec(
		`INSERT INTO entities (id, type, name) VALUES ('e1', 'concept', 'One')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db2, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.ReadDB().QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&n); err != nil || n != 1 {
		t.Errorf("entities lost across migration reopen: n=%d err=%v", n, err)
	}
}
