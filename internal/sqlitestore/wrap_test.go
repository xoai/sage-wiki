package sqlitestore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// P3-7 T1: Wrap produces a ModeWriter Backend over the caller's live handle
// without owning it — Close is a no-op, writes keep working after it.
func TestWrapBackend(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	b := Wrap(db, dir, Options{})
	var _ store.Backend = b

	if got := b.Location(); got != dir {
		t.Errorf("Location() = %q, want %q", got, dir)
	}

	// ModeWriter: WriteTx works.
	if err := b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO entities (id, type, name) VALUES ('w1', 'concept', 'W')`)
		return err
	}); err != nil {
		t.Fatalf("WriteTx on wrapped backend: %v", err)
	}

	// Close is a no-op: the caller's handle must still work afterwards.
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.ReadDB().Ping(); err != nil {
		t.Error("Close closed the caller's handle — Wrap must not own it")
	}
}

// Wrap with a non-writable handle is the caller's problem (documented
// sqlite-only assertion lives in wiki.Reconcile, not here).
func TestWrapOptionsTemporal(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	off := false
	b := Wrap(db, dir, Options{TemporalEnabled: &off})
	if got := b.Ontology(); got == nil {
		t.Error("Ontology must be constructed from the wrapped handle")
	}
}
