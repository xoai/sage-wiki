package storage

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify schema version
	var version int
	err = db.ReadDB().QueryRow("SELECT MAX(version) FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != 6 {
		t.Errorf("expected schema version 6, got %d", version)
	}

	// Verify tables exist
	tables := []string{"entries", "vec_entries", "entities", "relations", "learnings", "chunks_meta", "chunks_fts", "vec_chunks", "compile_items", "pending_outputs", "confirmation_sources"}
	for _, table := range tables {
		var name string
		err := db.ReadDB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type IN ('table', 'shadow') AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestIdempotentMigration(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open twice — second open should not re-run migration
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	db1.Close()

	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed: %v", err)
	}
	defer db2.Close()

	var count int
	err = db2.ReadDB().QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Errorf("expected 6 schema_version rows, got %d", count)
	}
}

func TestWriteTxSerialization(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a test table
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE test_counter (val INTEGER)")
		return err
	})
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO test_counter VALUES (0)")
		return err
	})

	// Run concurrent writes — should serialize via mutex
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.WriteTx(func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE test_counter SET val = val + 1")
				return err
			})
		}()
	}
	wg.Wait()

	var val int
	db.ReadDB().QueryRow("SELECT val FROM test_counter").Scan(&val)
	if val != 10 {
		t.Errorf("expected counter 10, got %d", val)
	}
}

func TestConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert test data
	db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO entities (id, type, name, created_at, updated_at)
			VALUES ('e1', 'concept', 'test', datetime('now'), datetime('now'))`)
		return err
	})

	// Concurrent reads should not block
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var name string
			db.ReadDB().QueryRow("SELECT name FROM entities WHERE id='e1'").Scan(&name)
			if name != "test" {
				t.Errorf("expected 'test', got %q", name)
			}
		}()
	}
	wg.Wait()
}

// TestOpen_CreatesParentDir verifies that Open creates a missing parent
// directory so callers don't need to MkdirAll first (fixes #84 obs 1).
func TestOpen_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	// .sage/ does not exist yet — mirror the post-`rm -rf .sage/` state
	dbPath := filepath.Join(dir, ".sage", "wiki.db")

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open with missing parent dir: %v", err)
	}
	defer db.Close()

	// Confirm the parent dir was created
	if _, err := db.WriteDB().Exec("CREATE TABLE t (id INTEGER)"); err != nil {
		t.Fatalf("write to db: %v", err)
	}
}
