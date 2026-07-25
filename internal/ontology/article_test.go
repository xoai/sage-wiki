package ontology

import (
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func typedStore(t *testing.T, valid []string) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db, nil, valid)
}

// The article's declared type must survive a reindex. The key is `entity_type`
// (what write.go emits); a `type:` parse would never match and every article
// would silently re-index as a concept.
func TestArticleEntityTypeReadsFrontmatter(t *testing.T) {
	s := typedStore(t, []string{TypeConcept, TypeTechnique})

	article := "---\nconcept: self-attention\nentity_type: technique\naliases: []\n---\n\n# Self Attention\n"
	if got := ArticleEntityType(article, s); got != TypeTechnique {
		t.Errorf("ArticleEntityType = %q, want %q", got, TypeTechnique)
	}

	// The wrong key must not be read — this is the regression guard.
	wrongKey := "---\nconcept: x\ntype: technique\n---\n\nbody\n"
	if got := ArticleEntityType(wrongKey, s); got != TypeConcept {
		t.Errorf("a bare `type:` key should not be honored, got %q", got)
	}
}

func TestArticleEntityTypeFallsBackToConcept(t *testing.T) {
	s := typedStore(t, []string{TypeConcept, TypeTechnique})

	for name, content := range map[string]string{
		"no frontmatter":    "# Just a heading\n\nbody\n",
		"no entity_type":    "---\nconcept: x\naliases: []\n---\n\nbody\n",
		"unterminated":      "---\nentity_type: technique\n",
		"empty entity_type": "---\nentity_type:\n---\n\nbody\n",
	} {
		if got := ArticleEntityType(content, s); got != TypeConcept {
			t.Errorf("%s: got %q, want %q", name, got, TypeConcept)
		}
	}
}

// A stale type — one the project no longer configures, e.g. after a pack is
// uninstalled — must fall back rather than be passed through. AddEntity
// rejects an out-of-set type, and reconcile RETURNS that error rather than
// warning, so passing it through would make that article fail to reindex on
// every run, forever.
func TestArticleEntityTypeRejectsStaleType(t *testing.T) {
	s := typedStore(t, []string{TypeConcept, TypeTechnique})

	stale := "---\nentity_type: pattern\n---\n\nbody\n"
	if got := ArticleEntityType(stale, s); got != TypeConcept {
		t.Errorf("stale type = %q, want fallback to %q", got, TypeConcept)
	}

	// And it really would have failed: the store rejects it.
	if err := s.AddEntity(Entity{ID: "x", Type: "pattern", Name: "X"}); err == nil {
		t.Error("expected AddEntity to reject an unconfigured type — the fallback exists because it does")
	}
}

func TestFormatConceptName(t *testing.T) {
	for in, want := range map[string]string{
		"self-attention":  "Self Attention",
		"backpressure":    "Backpressure",
		"a-b-c":           "A B C",
		"":                "",
		"已实现-concept":     "已实现 Concept",
		"leading--double": "Leading  Double",
	} {
		if got := FormatConceptName(in); got != want {
			t.Errorf("FormatConceptName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A CRLF-authored article must not lose its declared type. Because AddEntity
// now writes `type` unconditionally, a missed frontmatter match demotes the
// entity on EVERY reconcile — the failure this helper exists to prevent,
// reached through line endings instead of the wrong key.
func TestArticleEntityTypeHandlesCRLF(t *testing.T) {
	s := typedStore(t, []string{TypeConcept, TypeTechnique})

	crlf := "---\r\nconcept: self-attention\r\nentity_type: technique\r\n---\r\n\r\n# Self Attention\r\n"
	if got := ArticleEntityType(crlf, s); got != TypeTechnique {
		t.Errorf("CRLF article: got %q, want %q", got, TypeTechnique)
	}
}
