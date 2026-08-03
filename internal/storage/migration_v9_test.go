package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// buildV8Fixture creates a V8 DB from the real migrationV1..V8 consts
// (mirroring migration_v8_test.go's runner-mirroring approach) plus
// representative compile_items rows at every tier-completeness corner.
func buildV8Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v8.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{migrationV1, migrationV2, migrationV3, migrationV4, migrationV5, migrationV6, migrationV7, migrationV8}
	for i, sqlText := range consts {
		if i == 3 {
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

	rows := []struct {
		path                           string
		tier                           int
		indexed, embedded, parsed      int
		summarized, extracted, written int
	}{
		{"t0-done.md", 0, 1, 0, 0, 0, 0, 0},
		{"t0-pending.md", 0, 0, 0, 0, 0, 0, 0},
		{"t1-done.md", 1, 1, 1, 0, 0, 0, 0},
		{"t1-noembed.md", 1, 1, 0, 0, 0, 0, 0},
		{"t2-done.md", 2, 1, 1, 1, 0, 0, 0},
		{"t2-noparse.md", 2, 1, 1, 0, 0, 0, 0},
		{"t3-done.md", 3, 1, 1, 0, 1, 1, 1},
		{"t3-passes-but-no-embed.md", 3, 1, 0, 0, 1, 1, 1},
		{"t3-unwritten.md", 3, 1, 1, 0, 1, 1, 0},
	}
	for _, r := range rows {
		if _, err := raw.Exec(`INSERT INTO compile_items
			(source_path, tier, pass_indexed, pass_embedded, pass_parsed, pass_summarized, pass_extracted, pass_written)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.path, r.tier, r.indexed, r.embedded, r.parsed, r.summarized, r.extracted, r.written); err != nil {
			t.Fatalf("insert %s: %v", r.path, err)
		}
	}
	raw.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return reopened
}

func TestMigrationV9_Upgrade(t *testing.T) {
	db := buildV8Fixture(t)
	defer db.Close()

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 15 {
		t.Errorf("MAX(version) = %d, want 15", version)
	}

	// Queue columns exist with the right defaults.
	var tableSQL string
	if err := db.ReadDB().QueryRow("SELECT sql FROM sqlite_master WHERE name='compile_items'").Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"status", "lease_owner", "lease_until", "heartbeat_at", "attempts"} {
		if !strings.Contains(tableSQL, col) {
			t.Errorf("compile_items missing column %q: %s", col, tableSQL)
		}
	}
	var idxCount int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='idx_ci_claim'").Scan(&idxCount); err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Errorf("idx_ci_claim index missing")
	}

	// Backfill: per-tier completeness predicate.
	want := map[string]string{
		"t0-done.md":                "done",
		"t0-pending.md":             "pending",
		"t1-done.md":                "done",
		"t1-noembed.md":             "pending",
		"t2-done.md":                "done",
		"t2-noparse.md":             "pending",
		"t3-done.md":                "done",
		"t3-passes-but-no-embed.md": "pending",
		"t3-unwritten.md":           "pending",
	}
	for path, wantStatus := range want {
		var status string
		var attempts int
		if err := db.ReadDB().QueryRow("SELECT status, attempts FROM compile_items WHERE source_path=?", path).Scan(&status, &attempts); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if status != wantStatus {
			t.Errorf("%s status = %q, want %q", path, status, wantStatus)
		}
		if attempts != 0 {
			t.Errorf("%s attempts = %d, want 0", path, attempts)
		}
	}
}

func TestMigrationV9_FreshDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 15 {
		t.Errorf("fresh DB MAX(version) = %d, want 15", version)
	}
	var idxCount int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='idx_ci_claim'").Scan(&idxCount); err != nil {
		t.Fatal(err)
	}
	if idxCount != 1 {
		t.Errorf("fresh DB missing idx_ci_claim")
	}
}
