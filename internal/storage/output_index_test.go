package storage

import (
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestMigrationV7CreatesOutputIndex proves the forward migration adds the
// output_index table and advances the schema version.
func TestMigrationV7CreatesOutputIndex(t *testing.T) {
	db := openTestDB(t)

	var n int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM output_index").Scan(&n); err != nil {
		t.Fatalf("output_index table missing after migrate: %v", err)
	}

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version < 7 {
		t.Errorf("schema version = %d, want >= 7", version)
	}
}

// TestMigrationV7OnOldSchema proves an upgrade from a pre-V7 database: with the
// table dropped and the version rolled back to 6, reopening re-applies V7. The
// runner is forward-only — this asserts upgrade, not down-reversibility.
func TestMigrationV7OnOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Simulate an old (V6) database. The version rollback must also drop
	// V9's queue columns (and their index first) and V10's evidence columns:
	// sqlite ALTER ADD COLUMN is not re-runnable, and a faithful V6 database
	// never had them.
	if _, err := db.WriteDB().Exec("DROP INDEX IF EXISTS idx_ci_claim"); err != nil {
		t.Fatalf("drop v9 index: %v", err)
	}
	for _, col := range []string{"status", "lease_owner", "lease_until", "heartbeat_at", "attempts"} {
		if _, err := db.WriteDB().Exec("ALTER TABLE compile_items DROP COLUMN " + col); err != nil {
			t.Fatalf("drop v9 column %s: %v", col, err)
		}
	}
	for _, col := range []string{"compile_key", "compile_key_parts"} {
		if _, err := db.WriteDB().Exec("ALTER TABLE compile_items DROP COLUMN " + col); err != nil {
			t.Fatalf("drop v15 column %s: %v", col, err)
		}
	}
	for _, col := range []string{"evidence", "confidence", "source_doc", "valid_from", "valid_to", "invalidated_by"} {
		if _, err := db.WriteDB().Exec("ALTER TABLE relations DROP COLUMN " + col); err != nil {
			t.Fatalf("drop v10 column %s: %v", col, err)
		}
	}
	if _, err := db.WriteDB().Exec("DROP TABLE output_index"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := db.WriteDB().Exec("DELETE FROM schema_version WHERE version >= 7"); err != nil {
		t.Fatalf("rollback version: %v", err)
	}
	db.Close()

	// Reopen → the forward migration re-applies V7.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var n int
	if err := db2.ReadDB().QueryRow("SELECT COUNT(*) FROM output_index").Scan(&n); err != nil {
		t.Errorf("output_index not recreated on upgrade: %v", err)
	}
}

func TestOutputIndexCRUD(t *testing.T) {
	db := openTestDB(t)
	oi := NewOutputIndex(db)

	// Missing key.
	if _, ok, err := oi.Get("wiki/concepts/x.md"); err != nil || ok {
		t.Fatalf("Get missing: ok=%v err=%v", ok, err)
	}

	// Set then Get.
	if err := oi.Set("wiki/concepts/x.md", "hashA"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if h, ok, err := oi.Get("wiki/concepts/x.md"); err != nil || !ok || h != "hashA" {
		t.Fatalf("Get after set: h=%q ok=%v err=%v", h, ok, err)
	}

	// Set again upserts.
	if err := oi.Set("wiki/concepts/x.md", "hashB"); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	if h, _, _ := oi.Get("wiki/concepts/x.md"); h != "hashB" {
		t.Errorf("expected upsert to hashB, got %q", h)
	}

	// All.
	oi.Set("wiki/summaries/y.md", "hashY")
	all, err := oi.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 || all["wiki/concepts/x.md"] != "hashB" || all["wiki/summaries/y.md"] != "hashY" {
		t.Errorf("All = %v", all)
	}

	// Delete.
	if err := oi.Delete("wiki/concepts/x.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := oi.Get("wiki/concepts/x.md"); ok {
		t.Error("expected deleted key to be gone")
	}
}

// TestBackfillOutputIndex proves the backfill records the hash of the on-disk
// output bytes (the same bytes the reconciler re-hashes) only for outputs that
// have no row yet, leaving existing rows untouched — so the first reconcile
// after an upgrade finds nothing spuriously "changed".
func TestBackfillOutputIndex(t *testing.T) {
	db := openTestDB(t)
	oi := NewOutputIndex(db)

	// An output already recorded (e.g. a prior reconcile) must not be overwritten.
	if err := oi.Set("wiki/concepts/existing.md", "already"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	outputs := map[string][]byte{
		"wiki/concepts/existing.md": []byte("new content — must NOT overwrite"),
		"wiki/summaries/fresh.md":   []byte("fresh summary content"),
	}
	if err := oi.Backfill(outputs); err != nil {
		t.Fatalf("Backfill: %v", err)
	}

	if h, _, _ := oi.Get("wiki/concepts/existing.md"); h != "already" {
		t.Errorf("backfill overwrote an existing row: %q", h)
	}
	want := HashBytes([]byte("fresh summary content"))
	if h, ok, _ := oi.Get("wiki/summaries/fresh.md"); !ok || h != want {
		t.Errorf("backfill hash = %q, want %q (ok=%v)", h, want, ok)
	}
}
