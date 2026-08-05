package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/storage"
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

// copyFixture copies a testdata fixture directory to dst.
func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
}

// buildFixtureDB creates the fixture's SQLite store at the CURRENT schema
// version in dst (writer open, then close). The v0.2.x fixture carries only
// config + manifest in git (the .sage dir is ignored); a read-only engine
// open needs a present, current-schema DB or it fails with
// ErrSchemaVersionMismatch. Building it hermetically at test time makes the
// pre-format tests pass on fresh clones AND after any future migration —
// the pre-format discriminator is the manifest, never the DB.
func buildFixtureDB(t *testing.T, dst string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(dst, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("build fixture db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
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
// Uses the committed v0.2.x fixture (testdata/v02x — config + stripped
// manifest + real DB), not a synthesized one (Gate 8 S3).
func TestPreFormatReadOnlyAndUpgrade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	copyFixture(t, "testdata/v02x", dir)
	buildFixtureDB(t, dir)

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

// SPEC-08 D1: limits resolution — config block, WithLimits tightening,
// and ErrDocTooLarge back-compat.

func TestResolvedLimitsDefaults(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	got := w.ResolvedLimits()
	want := Limits{}.Resolve()
	if got != want {
		t.Fatalf("ResolvedLimits() = %+v, want %+v", got, want)
	}
}

func TestWithLimitsTightensPerField(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir, WithLimits(Limits{MaxDocBytes: 1024}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	got := w.ResolvedLimits()
	if got.MaxDocBytes != 1024 {
		t.Errorf("MaxDocBytes = %d, want 1024 (option override)", got.MaxDocBytes)
	}
	if got.MaxQueryBytes != DefaultMaxQueryBytes {
		t.Errorf("MaxQueryBytes = %d, want default %d (unset option fields stay)", got.MaxQueryBytes, DefaultMaxQueryBytes)
	}
}

func TestLimitsFromWorkspaceConfig(t *testing.T) {
	dir := initWorkspace(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_doc_bytes: 2048\n  max_query_bytes: 4096\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	got := w.ResolvedLimits()
	if got.MaxDocBytes != 2048 || got.MaxQueryBytes != 4096 {
		t.Errorf("config limits = %d/%d, want 2048/4096", got.MaxDocBytes, got.MaxQueryBytes)
	}
}

func TestWithLimitsOverridesConfigPerField(t *testing.T) {
	dir := initWorkspace(t)
	cfgPath := filepath.Join(dir, "config.yaml")
	old, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, append(old, []byte("limits:\n  max_doc_bytes: 2048\n  max_query_bytes: 4096\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Option overrides ONLY the fields it sets; the rest stay at config values.
	w, err := Open(context.Background(), dir, WithLimits(Limits{MaxDocBytes: 512}))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	got := w.ResolvedLimits()
	if got.MaxDocBytes != 512 {
		t.Errorf("MaxDocBytes = %d, want 512 (option wins)", got.MaxDocBytes)
	}
	if got.MaxQueryBytes != 4096 {
		t.Errorf("MaxQueryBytes = %d, want 4096 (config survives unset option field)", got.MaxQueryBytes)
	}
}

func TestErrDocTooLargeBackCompat(t *testing.T) {
	// The exported sentinel must remain errors.Is-reachable, now via the
	// LimitError's Unwrap (variable identity through the alias).
	err := fmt.Errorf("capture: %w", &LimitError{Which: "doc_bytes", Limit: 10, Got: 20})
	if !errors.Is(err, ErrDocTooLarge) {
		t.Fatal("errors.Is(wrapped LimitError, ErrDocTooLarge) = false, want true")
	}
}
