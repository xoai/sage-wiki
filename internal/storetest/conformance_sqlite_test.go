package storetest

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/sqlitestore"
	"github.com/xoai/sage-wiki/internal/store"
)

// TestConformanceSQLite runs the conformance suite against the sqlite
// backend (always, no env gate).
func TestConformanceSQLite(t *testing.T) {
	RunConformance(t, func(t *testing.T) store.Backend {
		t.Helper()
		b, err := sqlitestore.Open(t.TempDir(), store.ModeWriter, sqlitestore.Options{})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	})
}

// TestReaderModeSQLite pins reader-mode semantics on sqlite (spec §2):
// skip migrations, verify schema_version, ErrReadOnly on write paths,
// nil WriteDB.
func TestReaderModeSQLite(t *testing.T) {
	dir := t.TempDir()

	// Reader on missing schema fails.
	if _, err := sqlitestore.Open(dir, store.ModeReader, sqlitestore.Options{}); err == nil {
		t.Fatal("reader on empty vault: expected error, got nil")
	}

	// Seed via writer.
	w, err := sqlitestore.Open(dir, store.ModeWriter, sqlitestore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Entries().Add(store.Entry{ID: "src:x", Content: "seeded"}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	r, err := sqlitestore.Open(dir, store.ModeReader, sqlitestore.Options{})
	if err != nil {
		t.Fatalf("reader open: %v", err)
	}
	defer r.Close()

	if r.WriteDB() != nil {
		t.Error("reader WriteDB non-nil")
	}
	if err := r.WriteTx(nil); err == nil {
		t.Error("reader WriteTx: expected ErrReadOnly")
	}
	if _, err := r.BeginWrite(); err == nil {
		t.Error("reader BeginWrite: expected ErrReadOnly")
	}
	got, err := r.Entries().Get("src:x")
	if err != nil || got == nil {
		t.Error("reader cannot read seeded entry")
	}
}
