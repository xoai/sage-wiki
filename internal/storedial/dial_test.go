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
	// Without a reachable DSN, postgres dispatch fails at parse/connect —
	// NOT with "unknown backend" (dispatch is wired since T13).
	_, err := Open(config.StorageConfig{Backend: "postgres", DSN: "invalid-dsn", VectorDimension: 768},
		store.OpenOptions{Mode: store.ModeWriter, ProjectDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "parse dsn") {
		t.Fatalf("postgres dispatch err = %v, want parse failure", err)
	}
}

func TestOpenProjectPostgresConfig(t *testing.T) {
	dir := writeProject(t, `
storage:
  backend: postgres
  dsn: invalid-dsn
  vector_dimension: 768
`)
	_, err := OpenProject(dir, store.ModeWriter)
	if err == nil || !strings.Contains(err.Error(), "parse dsn") {
		t.Fatalf("err = %v, want parse failure", err)
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

func TestStoreDial_ThreadsVectorBackend(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(config.StorageConfig{Backend: "sqlite"}, store.OpenOptions{
		Mode:          store.ModeWriter,
		ProjectDir:    dir,
		VectorBackend: "mmap",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()
	vs, ok := b.Vectors().(interface{ VectorBackend() string })
	if !ok {
		t.Fatal("backend Vectors() must expose VectorBackend()")
	}
	if got := vs.VectorBackend(); got != "mmap" {
		t.Errorf("VectorBackend = %q, want mmap (threaded through OpenOptions)", got)
	}

	// Default (unset) stays memory.
	b2, err := Open(config.StorageConfig{Backend: "sqlite"}, store.OpenOptions{
		Mode:       store.ModeWriter,
		ProjectDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Open default: %v", err)
	}
	defer b2.Close()
	if got := b2.Vectors().(interface{ VectorBackend() string }).VectorBackend(); got != "memory" {
		t.Errorf("default VectorBackend = %q, want memory", got)
	}
}
