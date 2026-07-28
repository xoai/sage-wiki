package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildV12Fixture creates a V12 DB from the real migration consts (never
// hand-copied SQL), then Open runs the pending V13 migration.
func buildV12Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v12.db")

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
		migrationV11, migrationV12,
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
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open v12 fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// V-M3a: V13 creates the entry_dates sidecar on a pre-V13 DB; rows
// round-trip; a missing row is simply absent (no date — old DBs behave
// exactly as today until reconcile backfills).
func TestMigrationV13EntryDates(t *testing.T) {
	db := buildV12Fixture(t)

	// Table exists and accepts the sidecar shape.
	if err := db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO entry_dates (id, source_date) VALUES (?, ?)", "concept:x", 1700000000)
		return err
	}); err != nil {
		t.Fatalf("insert into entry_dates after V13: %v", err)
	}

	var got int64
	if err := db.ReadDB().QueryRow("SELECT source_date FROM entry_dates WHERE id = ?", "concept:x").Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != 1700000000 {
		t.Errorf("source_date = %d, want 1700000000", got)
	}

	// Missing row: no error semantics at the SQL layer — callers get
	// sql.ErrNoRows and treat it as "no date".
	err := db.ReadDB().QueryRow("SELECT source_date FROM entry_dates WHERE id = ?", "concept:absent").Scan(&got)
	if err != sql.ErrNoRows {
		t.Errorf("missing row: err = %v, want sql.ErrNoRows", err)
	}

	// Upsert semantics (compile re-runs update the date).
	if err := db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO entry_dates (id, source_date) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET source_date = excluded.source_date",
			"concept:x", 1800000000)
		return err
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.ReadDB().QueryRow("SELECT source_date FROM entry_dates WHERE id = ?", "concept:x").Scan(&got); err != nil || got != 1800000000 {
		t.Errorf("after upsert: %d %v, want 1800000000", got, err)
	}
}
