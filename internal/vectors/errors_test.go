package vectors

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// TestGet_ClosedDB_ReturnsError pins REL-04: a real DB error (closed
// database) must surface as a wrapped error, NOT masquerade as (nil, nil)
// "not found".
func TestGet_ClosedDB_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	s := NewStore(db)

	// Seed a row so the table exists, then close the DB out from under the store.
	if err := s.Upsert("a", []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	db.Close()

	vec, err := s.Get("a")
	if err == nil {
		t.Fatalf("Get on closed DB: expected error, got nil (vec=%v) — real errors must not masquerade as not-found", vec)
	}
	if !strings.Contains(err.Error(), "vectors.Get") {
		t.Errorf("error should be wrapped with vectors.Get context, got: %v", err)
	}
}

// TestGet_MissingID_StillNilNil is the regression guard: a genuine cache
// MISS on a healthy DB stays (nil, nil) with no error — the ErrNoRows path
// D1 preserves.
func TestGet_MissingID_StillNilNil(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	s := NewStore(db)

	vec, err := s.Get("never-existed")
	if err != nil {
		t.Fatalf("Get missing ID: expected nil error, got %v", err)
	}
	if vec != nil {
		t.Errorf("Get missing ID: expected nil vector, got %v", vec)
	}

	// And a present ID still decodes.
	stored := []float32{0.5, 0.5, 0.5}
	if err := s.Upsert("x", stored); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	vec, err = s.Get("x")
	if err != nil || vec == nil {
		t.Fatalf("Get present ID: vec=%v err=%v", vec, err)
	}
	if len(vec) != len(stored) || vec[0] != stored[0] {
		t.Errorf("Get present ID: vec=%v, want %v", vec, stored)
	}
}

// TestGet_CorruptBlob_ReturnsError closes the decode-layer hole in the
// REL-04 contract (independent verification): a blob that is empty or not a
// multiple of 4 bytes cannot be a valid embedding — it must surface as an
// error, NOT decode silently to a (non-nil!) empty/garbage vector that
// callers like dedup Seed would count as a cache HIT.
func TestGet_CorruptBlob_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	s := NewStore(db)

	// Plant corrupt blobs directly (Upsert only writes valid ones).
	for name, blob := range map[string][]byte{
		"short":  {1, 2, 3},
		"ragged": {1, 2, 3, 4, 5},
	} {
		if _, err := db.WriteDB().Exec("INSERT OR REPLACE INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)", "corrupt-"+name, blob, len(blob)/4); err != nil {
			t.Fatalf("plant corrupt blob: %v", err)
		}
		vec, err := s.Get("corrupt-" + name)
		if err == nil {
			t.Errorf("blob %v: expected corrupt-blob error, got vec=%v (len %d) nil — corrupt data must not masquerade as a hit", blob, vec, len(vec))
		}
	}
}
