package hub

import (
	"path/filepath"
	"testing"
)

func TestNewAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.yaml")

	cfg := New()
	cfg.AddProject("main", Project{Path: "/tmp/wiki", Description: "Main", Searchable: true})

	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Projects["main"].Path != "/tmp/wiki" {
		t.Errorf("path not persisted: %q", loaded.Projects["main"].Path)
	}
}

func TestSearchableProjects(t *testing.T) {
	cfg := New()
	cfg.AddProject("a", Project{Path: "/a", Searchable: true})
	cfg.AddProject("b", Project{Path: "/b", Searchable: false})
	cfg.AddProject("c", Project{Path: "/c", Searchable: true})

	s := cfg.SearchableProjects()
	if len(s) != 2 {
		t.Errorf("expected 2 searchable, got %d", len(s))
	}
}

func TestRemoveProject(t *testing.T) {
	cfg := New()
	cfg.AddProject("x", Project{Path: "/x"})
	cfg.RemoveProject("x")
	if len(cfg.Projects) != 0 {
		t.Error("not removed")
	}
}
