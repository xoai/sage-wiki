package pack

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestListBundled(t *testing.T) {
	packs := ListBundled()
	if len(packs) != 8 {
		t.Fatalf("expected 8 bundled packs, got %d", len(packs))
	}

	names := make(map[string]bool)
	for _, p := range packs {
		names[p.Name] = true
		if p.Version == "" {
			t.Errorf("pack %q has empty version", p.Name)
		}
		if p.Description == "" {
			t.Errorf("pack %q has empty description", p.Name)
		}
		if p.Tier != "bundled" {
			t.Errorf("pack %q tier = %q, want \"bundled\"", p.Name, p.Tier)
		}
	}

	expected := []string{
		"academic-research",
		"software-engineering",
		"product-management",
		"personal-knowledge",
		"study-group",
		"meeting-organizer",
		"content-creation",
		"legal-compliance",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing bundled pack %q", name)
		}
	}
}

func TestIsBundled(t *testing.T) {
	if !IsBundled("academic-research") {
		t.Error("academic-research should be bundled")
	}
	if !IsBundled("software-engineering") {
		t.Error("software-engineering should be bundled")
	}
	if IsBundled("nonexistent-pack") {
		t.Error("nonexistent-pack should not be bundled")
	}
}

func TestGetBundled(t *testing.T) {
	manifest, packFS, err := GetBundled("academic-research")
	if err != nil {
		t.Fatalf("GetBundled error: %v", err)
	}
	if manifest.Name != "academic-research" {
		t.Errorf("name = %q, want academic-research", manifest.Name)
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", manifest.Version)
	}
	if len(manifest.Prompts) == 0 {
		t.Error("expected prompts in manifest")
	}

	// verify embedded FS contains prompt files
	for _, p := range manifest.Prompts {
		_, err := fs.ReadFile(packFS, "prompts/"+p)
		if err != nil {
			t.Errorf("prompt %q not found in embedded FS: %v", p, err)
		}
	}
}

func TestGetBundled_NotFound(t *testing.T) {
	_, _, err := GetBundled("nonexistent-pack")
	if err == nil {
		t.Error("expected error for nonexistent bundled pack")
	}
}

func TestGetBundled_AllPacks(t *testing.T) {
	packs := ListBundled()
	for _, p := range packs {
		t.Run(p.Name, func(t *testing.T) {
			manifest, packFS, err := GetBundled(p.Name)
			if err != nil {
				t.Fatalf("GetBundled(%q) error: %v", p.Name, err)
			}
			if manifest.Name != p.Name {
				t.Errorf("name mismatch: manifest=%q info=%q", manifest.Name, p.Name)
			}

			// verify all listed prompts exist
			for _, prompt := range manifest.Prompts {
				if _, err := fs.ReadFile(packFS, "prompts/"+prompt); err != nil {
					t.Errorf("prompt %q listed but not found", prompt)
				}
			}

			// verify all listed skills exist
			for _, skill := range manifest.Skills {
				if _, err := fs.ReadFile(packFS, "skills/"+skill); err != nil {
					t.Errorf("skill %q listed but not found", skill)
				}
			}

			// verify all listed samples exist
			for _, sample := range manifest.Samples {
				if _, err := fs.ReadFile(packFS, "samples/"+sample); err != nil {
					t.Errorf("sample %q listed but not found", sample)
				}
			}
		})
	}
}

func TestInstallBundled(t *testing.T) {
	cacheDir := t.TempDir()
	manifest, destDir, err := InstallBundled("academic-research", cacheDir)
	if err != nil {
		t.Fatalf("InstallBundled error: %v", err)
	}
	if manifest.Name != "academic-research" {
		t.Errorf("name = %q", manifest.Name)
	}

	expectedDir := filepath.Join(cacheDir, "academic-research")
	if destDir != expectedDir {
		t.Errorf("destDir = %q, want %q", destDir, expectedDir)
	}

	// verify pack.yaml exists in cache
	if _, err := os.Stat(filepath.Join(destDir, "pack.yaml")); err != nil {
		t.Error("pack.yaml not found in cache")
	}

	// verify prompts copied
	for _, p := range manifest.Prompts {
		if _, err := os.Stat(filepath.Join(destDir, "prompts", p)); err != nil {
			t.Errorf("prompt %q not copied to cache", p)
		}
	}
}

func TestInstall_BundledFirst(t *testing.T) {
	cacheDir := t.TempDir()
	manifest, _, err := Install("academic-research", cacheDir)
	if err != nil {
		t.Fatalf("Install bundled error: %v", err)
	}
	if manifest.Name != "academic-research" {
		t.Errorf("Install should resolve bundled pack, got %q", manifest.Name)
	}
}

func TestGetBundled_InfoFields(t *testing.T) {
	packs := ListBundled()
	for _, p := range packs {
		t.Run(p.Name, func(t *testing.T) {
			manifest, _, err := GetBundled(p.Name)
			if err != nil {
				t.Fatalf("GetBundled error: %v", err)
			}
			if manifest.Author == "" {
				t.Error("author should not be empty")
			}
			if len(manifest.Tags) == 0 {
				t.Error("tags should not be empty")
			}
			if len(manifest.Ontology.EntityTypes) == 0 {
				t.Error("entity types should not be empty")
			}
			if len(manifest.Ontology.RelationTypes) == 0 {
				t.Error("relation types should not be empty")
			}
		})
	}
}
