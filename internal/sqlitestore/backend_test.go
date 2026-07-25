package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/store"
)

func openWriter(t *testing.T) store.Backend {
	t.Helper()
	b, err := Open(t.TempDir(), store.ModeWriter, Options{})
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestWriterOpenProvidesAllStores(t *testing.T) {
	b := openWriter(t)
	if b.Entries() == nil || b.Chunks() == nil || b.Vectors() == nil ||
		b.Ontology() == nil || b.Trust() == nil || b.CompileItems() == nil ||
		b.OutputIndex() == nil || b.Learnings() == nil {
		t.Fatal("writer backend missing a store")
	}
	if b.WriteDB() == nil {
		t.Fatal("writer WriteDB must be non-nil")
	}
	if !b.SchemaReady() {
		t.Error("SchemaReady false after writer open (migrations ran)")
	}
	if b.Location() == "" {
		t.Error("Location empty")
	}
	if err := b.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
}

func TestWriterRoundTripThroughInterfaces(t *testing.T) {
	b := openWriter(t)
	if err := b.Entries().Add(memory.Entry{ID: "e1", Content: "hello world"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := b.Entries().Get("e1")
	if err != nil || got == nil {
		t.Fatalf("Get: %v, %v", got, err)
	}
	if got.Content != "hello world" {
		t.Errorf("content = %q", got.Content)
	}
	n, err := b.CompileItems().Count()
	if err != nil || n != 0 {
		t.Errorf("CompileItems.Count = %d, %v", n, err)
	}
}

func TestReaderModeSemantics(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(dir, store.ModeWriter, Options{})
	if err != nil {
		t.Fatalf("writer open: %v", err)
	}
	if err := w.Entries().Add(memory.Entry{ID: "e1", Content: "x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w.Close()

	r, err := Open(dir, store.ModeReader, Options{})
	if err != nil {
		t.Fatalf("reader open: %v", err)
	}
	defer r.Close()

	if r.WriteDB() != nil {
		t.Error("reader WriteDB must be nil")
	}
	if err := r.WriteTx(func(tx *sql.Tx) error { return nil }); !errors.Is(err, store.ErrReadOnly) {
		t.Errorf("reader WriteTx err = %v, want ErrReadOnly", err)
	}
	if _, err := r.BeginWrite(); !errors.Is(err, store.ErrReadOnly) {
		t.Errorf("reader BeginWrite err = %v, want ErrReadOnly", err)
	}
	// Reads work in reader mode.
	got, err := r.Entries().Get("e1")
	if err != nil || got == nil {
		t.Errorf("reader Get: %v, %v", got, err)
	}
}

func TestReaderOpenOnMissingSchemaFails(t *testing.T) {
	_, err := Open(t.TempDir(), store.ModeReader, Options{})
	if err == nil {
		t.Fatal("reader open on empty dir: expected schema error, got nil")
	}
}

func TestBeginWriteMutexReleased(t *testing.T) {
	b := openWriter(t)
	tx, err := b.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS bw (id TEXT)"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Released: WriteTx proceeds.
	if err := b.WriteTx(func(tx *sql.Tx) error { return nil }); err != nil {
		t.Errorf("WriteTx after BeginWrite commit blocked: %v", err)
	}
	tx2, err := b.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite 2: %v", err)
	}
	tx2.Rollback()
}

func TestUnwrap(t *testing.T) {
	b := openWriter(t)
	if Unwrap(b) == nil {
		t.Error("Unwrap returned nil for sqlite backend")
	}
}

func TestOpenOptions_ANNThreadsToStore(t *testing.T) {
	b, err := OpenPath(filepath.Join(t.TempDir(), "wiki.db"), store.ModeWriter, Options{ANN: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()
	vec, ok := b.Vectors().(interface{ IndexKind() string })
	if !ok {
		t.Fatal("Vectors() does not expose IndexKind — the option plumbing is missing")
	}
	if vec.IndexKind() == "brute-force" {
		t.Error("Options{ANN: true} must reach the vectors.Store construction")
	}
}
