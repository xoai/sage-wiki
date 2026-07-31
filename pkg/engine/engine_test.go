package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// initWorkspace builds a real workspace on disk via InitGreenfield (through
// Init) and returns its dir.
func initWorkspace(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ws")
	w, err := Init(context.Background(), dir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

// stripFormatFields rewrites .manifest.json without the format fields,
// simulating a v0.2.x workspace.
func stripFormatFields(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "format_version")
	delete(m, "engine_version")
	delete(m, "created_at")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCloseIdempotent(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}
	if err := w.checkOpen(); err == nil {
		t.Error("use after Close must error")
	}
}

func TestOpenNotInitialized(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err = %v, want ErrNotInitialized", err)
	}
}

func TestOpenConcurrentSecondFails(t *testing.T) {
	dir := initWorkspace(t)
	w1, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer w1.Close()
	if _, err := Open(context.Background(), dir); !errors.Is(err, ErrLocked) {
		t.Errorf("second Open = %v, want ErrLocked", err)
	}
}

func TestOpenReadOnlyCoexistsWithWriter(t *testing.T) {
	dir := initWorkspace(t)
	w1, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("writer Open: %v", err)
	}
	defer w1.Close()
	w2, err := Open(context.Background(), dir, WithReadOnly())
	if err != nil {
		t.Fatalf("read-only Open must coexist with a held writer lock: %v", err)
	}
	defer w2.Close()
	if err := w2.checkMutable(); !errors.Is(err, ErrReadOnly) {
		t.Errorf("checkMutable = %v, want ErrReadOnly", err)
	}
}

// TestPreFormatReadOnlyAndUpgrade is AC-B6: a v0.2.x workspace opens
// read-only; mutators return ErrIncompatibleVersion; WithUpgrade adopts.
func TestPreFormatReadOnlyAndUpgrade(t *testing.T) {
	dir := initWorkspace(t)
	stripFormatFields(t, dir)

	m, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsPreFormat() {
		t.Fatal("fixture must be pre-format")
	}

	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("pre-format Open must succeed read-only: %v", err)
	}
	if err := w.checkMutable(); !errors.Is(err, ErrIncompatibleVersion) {
		t.Errorf("checkMutable = %v, want ErrIncompatibleVersion", err)
	}
	w.Close()

	// A pre-format workspace takes no lock: two opens coexist.
	w2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	w3, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	w2.Close()
	w3.Close()

	// WithUpgrade adopts (one-way) and enables writes.
	w4, err := Open(context.Background(), dir, WithUpgrade())
	if err != nil {
		t.Fatalf("WithUpgrade Open: %v", err)
	}
	if err := w4.checkMutable(); err != nil {
		t.Errorf("post-upgrade checkMutable = %v, want nil", err)
	}
	w4.Close()

	m2, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m2.IsPreFormat() {
		t.Error("adoption must stamp the current format")
	}
}

func TestOpenConfigLoadErrorSentinel(t *testing.T) {
	dir := initWorkspace(t)
	// Corrupt the config: config.Load must fail and map to ErrConfigLoad.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(context.Background(), dir)
	if !errors.Is(err, ErrConfigLoad) {
		t.Errorf("err = %v, want ErrConfigLoad", err)
	}
}
