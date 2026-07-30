package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestReconcileStartupHealsDrift verifies the startup wiring both runCompile and
// runServe call: a concept article present on disk and in the manifest but not
// indexed in the DB gets re-indexed by reconcileStartup.
func TestReconcileStartupHealsDrift(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// A concept article on disk + in the manifest, but not indexed (drift).
	rel := filepath.Join(cfg.Output, "concepts", "orphan.md")
	abs := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(abs), 0755)
	if err := os.WriteFile(abs, []byte("# Orphan\n\nUnindexed article."), 0644); err != nil {
		t.Fatalf("write article: %v", err)
	}
	m := manifest.New()
	m.AddConcept("orphan", rel, []string{"raw/o.md"})
	if err := m.Save(filepath.Join(dir, ".manifest.json")); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	reconcileStartup(context.Background(), dir)

	// The article should now be indexed in FTS.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()
	if e, _ := memory.NewStore(db).Get("concept:orphan"); e == nil {
		t.Error("reconcileStartup did not index the drifted article")
	}
}

// TestReconcileStartupNoConfigNoPanic verifies startup reconcile is a safe no-op
// when the project is not initialized (no config / no db).
func TestReconcileStartupNoConfigNoPanic(t *testing.T) {
	dir := t.TempDir()
	reconcileStartup(context.Background(), dir) // must not panic or error out
}

// P3-7: the manifest-presence guard — a vault with config but NO manifest
// must not even open a backend (no stray wiki.db on sqlite, no advisory
// lock on PG).
func TestReconcileStartupSkipsWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	// InitGreenfield bootstraps the sqlite db file (P2-1 skip-listed init) —
	// remove it so the guard is what decides.
	os.Remove(filepath.Join(dir, ".sage", "wiki.db"))

	reconcileStartup(context.Background(), dir)

	if _, err := os.Stat(filepath.Join(dir, ".sage", "wiki.db")); !os.IsNotExist(err) {
		t.Error("guard absent: reconcileStartup created a wiki.db on a manifest-less vault")
	}
}
