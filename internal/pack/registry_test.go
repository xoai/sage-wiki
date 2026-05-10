package pack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistry_Search(t *testing.T) {
	// create a mock registry with index.yaml
	cacheDir := t.TempDir()
	repoDir := filepath.Join(cacheDir, "registry-cache", "repo")
	os.MkdirAll(repoDir, 0o755)

	// fake .git dir so FetchIndex thinks it's a cloned repo
	os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755)

	indexYAML := `packs:
  - name: academic-research
    version: 1.0.0
    description: Pack for academic research
    tier: official
    tags: [research, academic]
  - name: software-engineering
    version: 1.0.0
    description: Pack for software teams
    tier: official
    tags: [software, engineering]
  - name: personal-knowledge
    version: 0.9.0
    description: Zettelkasten-style knowledge base
    tier: community
    tags: [zettelkasten, personal]
`
	os.WriteFile(filepath.Join(repoDir, "index.yaml"), []byte(indexYAML), 0o644)

	reg := &Registry{CacheDir: cacheDir, RegistryURL: defaultRegistryURL}

	// search by name
	results, err := reg.Search("academic")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "academic-research" {
		t.Errorf("search 'academic': got %d results", len(results))
	}

	// search by tag
	results, err = reg.Search("zettelkasten")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Name != "personal-knowledge" {
		t.Errorf("search 'zettelkasten': got %d results", len(results))
	}

	// search by description
	results, err = reg.Search("software")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("search 'software': got %d results", len(results))
	}

	// search with no match
	results, err = reg.Search("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("search 'nonexistent': got %d results, want 0", len(results))
	}
}

func TestRegistry_StaleCache(t *testing.T) {
	cacheDir := t.TempDir()

	// write a stale cache index
	cacheIndexDir := filepath.Join(cacheDir, "registry-cache")
	os.MkdirAll(cacheIndexDir, 0o755)
	staleYAML := `packs:
  - name: stale-pack
    version: 0.1.0
    description: From stale cache
`
	os.WriteFile(filepath.Join(cacheIndexDir, "index.yaml"), []byte(staleYAML), 0o644)

	reg := &Registry{CacheDir: cacheDir, RegistryURL: "https://invalid.example.com/nonexistent"}

	// FetchIndex should fail to clone but fall back to stale cache
	packs, err := reg.FetchIndex()
	if err != nil {
		t.Fatalf("expected stale fallback, got error: %v", err)
	}
	if len(packs) != 1 || packs[0].Name != "stale-pack" {
		t.Errorf("stale fallback returned wrong data: %v", packs)
	}
}

func TestRegistry_FindInIndex(t *testing.T) {
	reg := NewRegistry(t.TempDir())
	packs := []PackInfo{
		{Name: "pack-a", Version: "1.0.0"},
		{Name: "pack-b", Version: "2.0.0"},
	}

	found := reg.FindInIndex("pack-b", packs)
	if found == nil || found.Version != "2.0.0" {
		t.Errorf("FindInIndex failed: %v", found)
	}

	notFound := reg.FindInIndex("pack-c", packs)
	if notFound != nil {
		t.Error("FindInIndex should return nil for missing pack")
	}
}
