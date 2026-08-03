package storage

import (
	"path/filepath"
	"testing"
)

func TestOpenReadOnlySkipsMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")

	// Reader open on a nonexistent/empty DB must fail with schema mismatch
	// (migrations NOT run — no schema_version table exists).
	if _, err := OpenWithOptions(path, OpenOptions{ReadOnly: true}); err == nil {
		t.Fatal("reader open on empty DB: expected error, got nil")
	}

	// Writer open creates schema; reader open then succeeds.
	w, err := Open(path)
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("writer close: %v", err)
	}

	r, err := OpenWithOptions(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reader open: %v", err)
	}
	defer r.Close()
}

func TestReadOnlyWriteTxFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	w.Close()

	r, err := OpenWithOptions(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reader open: %v", err)
	}
	defer r.Close()

	if err := r.WriteTx(nil); err == nil {
		t.Fatal("WriteTx on read-only DB: expected error, got nil")
	}
	if r.WriteDB() != nil {
		t.Fatal("WriteDB on read-only DB must return nil")
	}
}

func TestBeginWriteReleaseOnCommitAndRollback(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "wiki.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	tx1, err := db.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite 1: %v", err)
	}
	if _, err := tx1.Exec("CREATE TABLE IF NOT EXISTS bw_test (id TEXT)"); err != nil {
		t.Fatalf("exec in tx1: %v", err)
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("commit tx1: %v", err)
	}

	// Mutex released: second BeginWrite must not block.
	tx2, err := db.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite 2 after commit: %v", err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatalf("rollback tx2: %v", err)
	}

	// And again after rollback.
	tx3, err := db.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite 3 after rollback: %v", err)
	}
	tx3.Rollback()
}

func TestCurrentSchemaVersion(t *testing.T) {
	if v := CurrentSchemaVersion(); v != 15 {
		t.Errorf("CurrentSchemaVersion = %d, want 15 (V1–V15)", v)
	}
}
