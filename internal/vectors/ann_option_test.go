package vectors

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func TestWithANN_OptionAndIndexKind(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	def := NewStore(db)
	if def.IndexKind() != "brute-force" {
		t.Errorf("default IndexKind = %q, want brute-force", def.IndexKind())
	}
	off := NewStore(db, WithANN(false))
	if off.IndexKind() != "brute-force" {
		t.Errorf("WithANN(false) IndexKind = %q", off.IndexKind())
	}
	on := NewStore(db, WithANN(true))
	if on.IndexKind() == "brute-force" {
		t.Error("WithANN(true) must report the ANN kind")
	}
}
