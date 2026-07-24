package hub

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func setupTestDB(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, name, "test-model"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dir + "/.sage/wiki.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	memory.NewStore(db).Add(memory.Entry{
		ID: name + "-e1", Content: "test content about " + name,
		ArticlePath: "wiki/concepts/" + name + ".md",
	})
	return dir
}

func TestFederatedSearch(t *testing.T) {
	dir1 := setupTestDB(t, "alpha")
	dir2 := setupTestDB(t, "beta")

	projects := map[string]Project{
		"alpha": {Path: dir1, Searchable: true},
		"beta":  {Path: dir2, Searchable: true},
	}

	results, err := FederatedSearch(projects, "test content", 10)
	if err != nil {
		t.Fatalf("FederatedSearch: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results")
	}
	for _, r := range results {
		if r.Project == "" {
			t.Error("missing project field")
		}
	}
}

// TestMultiProjectReaderOpens verifies N>3 projects can be searched through
// the reader-mode path without resource exhaustion (spec done-when 7; the
// postgres pool-size leg runs under TEST_DATABASE_URL in T14).
func TestMultiProjectReaderOpens(t *testing.T) {
	projects := map[string]Project{}
	for _, name := range []string{"p1", "p2", "p3", "p4", "p5"} {
		dir := setupTestDB(t, name)
		projects[name] = Project{Path: dir, Searchable: true}
	}

	results, err := FederatedSearch(projects, "test content", 20)
	if err != nil {
		t.Fatalf("FederatedSearch: %v", err)
	}
	// Every project contributed at least one readable result.
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.Project] = true
	}
	for name := range projects {
		if !seen[name] {
			t.Errorf("project %s produced no results through reader path", name)
		}
	}
}

// TestSearchProject_ANNFlagNilSafe: a project WITHOUT config.yaml exercises
// the cfgErr != nil fallback — cfg is nil, the search must still work
// (brute-force) and never dereference cfg.
func TestSearchProject_ANNFlagNilSafe(t *testing.T) {
	dir := setupTestDB(t, "noconfig")
	os.Remove(filepath.Join(dir, "config.yaml"))

	results, err := searchProject(dir, "test content", 5)
	if err != nil {
		t.Fatalf("searchProject without config: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results from nil-cfg fallback path")
	}
}

// TestSearchProject_ANNEnabled: with search.ann.enabled: true the search
// runs over the HNSW backend and returns results.
func TestSearchProject_ANNEnabled(t *testing.T) {
	dir := setupTestDB(t, "ann")
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\nsearch:\n  ann:\n    enabled: true\n")...)
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := searchProject(dir, "test content", 5)
	if err != nil {
		t.Fatalf("searchProject with ANN: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results over the ANN backend")
	}
}
