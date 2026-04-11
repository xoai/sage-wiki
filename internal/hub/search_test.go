package hub

import (
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
