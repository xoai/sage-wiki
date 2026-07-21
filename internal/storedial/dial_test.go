package storedial

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/store"
)

const minimalConfig = `project: test
output: wiki
sources:
  - path: raw
    type: dir
`

func writeProject(t *testing.T, extraConfig string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(minimalConfig+extraConfig), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenSqliteDispatch(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(config.StorageConfig{Backend: "sqlite"}, store.OpenOptions{
		Mode:       store.ModeWriter,
		ProjectDir: dir,
	})
	if err != nil {
		t.Fatalf("Open sqlite: %v", err)
	}
	defer b.Close()
	if err := b.Entries().Add(memory.Entry{ID: "e1", Content: "x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got, _ := b.Entries().Get("e1"); got == nil {
		t.Fatal("round trip failed")
	}
}

func TestOpenPostgresNotAvailable(t *testing.T) {
	_, err := Open(config.StorageConfig{Backend: "postgres", DSN: "postgres://x", VectorDimension: 768},
		store.OpenOptions{Mode: store.ModeWriter, ProjectDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("postgres dispatch err = %v, want 'not available'", err)
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	_, err := Open(config.StorageConfig{Backend: "mysql"}, store.OpenOptions{ProjectDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unknown storage backend") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenProjectSqliteParity(t *testing.T) {
	dir := writeProject(t, "")
	b, err := OpenProject(dir, store.ModeWriter)
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	defer b.Close()
	// Byte-identical to storage.Open: file at the conventional path, migrated.
	if _, err := os.Stat(filepath.Join(dir, ".sage", "wiki.db")); err != nil {
		t.Errorf("db file missing: %v", err)
	}
	if !b.SchemaReady() {
		t.Error("SchemaReady false — migrations did not run")
	}
}

func TestOpenProjectReaderOnFreshProjectFails(t *testing.T) {
	dir := writeProject(t, "")
	if _, err := OpenProject(dir, store.ModeReader); err == nil {
		t.Fatal("reader on fresh project: expected schema error, got nil")
	}
}

func TestOpenProjectPostgresConfig(t *testing.T) {
	dir := writeProject(t, `
storage:
  backend: postgres
  dsn: postgres://u:p@host/db
  vector_dimension: 768
`)
	_, err := OpenProject(dir, store.ModeWriter)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want 'not available'", err)
	}
}
