package prompts

import (
	"os"
	"path/filepath"
	"testing"
)

// compileTemplates is the expected contents of the version table — the 6
// templates the compile pipeline actually renders (SPEC-04 template_key).
var compileTemplates = []string{
	"summarize_article",
	"summarize_paper",
	"extract_concepts",
	"write_article",
	"extract_triples",
	"resolve_entities",
}

// TestTemplateVersions_TableCoversCompileTemplates pins the table's
// contents: exactly the 6 compile templates, all semver-shaped.
func TestTemplateVersions_TableCoversCompileTemplates(t *testing.T) {
	versions := TemplateVersions()
	if len(versions) != len(compileTemplates) {
		t.Fatalf("TemplateVersions() has %d entries, want %d: %v", len(versions), len(compileTemplates), versions)
	}
	for _, name := range compileTemplates {
		v, ok := versions[name]
		if !ok {
			t.Errorf("TemplateVersions() missing %q", name)
			continue
		}
		if len(v) == 0 || v[0] < '0' || v[0] > '9' {
			t.Errorf("TemplateVersions()[%q] = %q, want semver", name, v)
		}
	}
}

// TestTemplateVersions_EveryEmbeddedCompileTemplateVersioned is the guard:
// a template existing in templates/ but not the table (and vice versa)
// fails — the table can never silently drift from the embed set.
func TestTemplateVersions_EveryEmbeddedCompileTemplateVersioned(t *testing.T) {
	versions := TemplateVersions()
	// The embedded set must contain every table entry.
	for name := range versions {
		if _, err := templateFS.ReadFile("templates/" + name + ".txt"); err != nil {
			t.Errorf("version table names %q but templates/%s.txt is not embedded: %v", name, name, err)
		}
	}
}

// TestEffectiveTemplateHashes_EmbeddedDefault pins hashes over embedded
// bytes; an override in prompts/ changes exactly that template's hash.
func TestEffectiveTemplateHashes_EmbeddedDefault(t *testing.T) {
	r := NewRegistry()
	hashes, err := EffectiveTemplateHashes(r)
	if err != nil {
		t.Fatalf("EffectiveTemplateHashes: %v", err)
	}
	if len(hashes) != len(compileTemplates) {
		t.Fatalf("got %d hashes, want %d", len(hashes), len(compileTemplates))
	}
	for name, h := range hashes {
		if len(h) != 16 {
			t.Errorf("hash for %q = %q, want 16 hex chars", name, h)
		}
	}

	// Same registry, same hashes (stable).
	hashes2, _ := EffectiveTemplateHashes(r)
	for name, h := range hashes {
		if hashes2[name] != h {
			t.Errorf("hash for %q changed between calls: %q vs %q", name, h, hashes2[name])
		}
	}
}

// TestEffectiveTemplateHashes_OverrideChangesExactlyOne pins override
// sensitivity per template.
func TestEffectiveTemplateHashes_OverrideChangesExactlyOne(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry()
	before, err := EffectiveTemplateHashes(r)
	if err != nil {
		t.Fatal(err)
	}

	// Override write_article via the prompts/ dir convention (dash form).
	if err := os.WriteFile(filepath.Join(dir, "write-article.md"), []byte("{{.ConceptName}} — overridden template body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	after, err := EffectiveTemplateHashes(r)
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	for name := range before {
		if before[name] != after[name] {
			changed++
			if name != "write_article" {
				t.Errorf("hash for %q changed without an override for it", name)
			}
		}
	}
	if changed != 1 {
		t.Errorf("%d hashes changed, want exactly 1 (write_article)", changed)
	}
}
