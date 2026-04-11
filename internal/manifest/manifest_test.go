package manifest

import (
	"path/filepath"
	"testing"
)

func TestNewManifest(t *testing.T) {
	m := New()
	if m.Version != 2 {
		t.Errorf("expected version 2, got %d", m.Version)
	}
	if len(m.Sources) != 0 {
		t.Error("expected empty sources")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	m := New()
	m.AddSource("raw/paper.pdf", "sha256:abc", "paper", 1024)
	m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf"})

	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.SourceCount() != 1 {
		t.Errorf("expected 1 source, got %d", loaded.SourceCount())
	}
	if loaded.ConceptCount() != 1 {
		t.Errorf("expected 1 concept, got %d", loaded.ConceptCount())
	}

	s := loaded.Sources["raw/paper.pdf"]
	if s.Hash != "sha256:abc" {
		t.Errorf("expected hash sha256:abc, got %q", s.Hash)
	}
	if s.Status != "pending" {
		t.Errorf("expected pending, got %q", s.Status)
	}
}

func TestLoadMissing(t *testing.T) {
	m, err := Load("/nonexistent/path/.manifest.json")
	if err != nil {
		t.Fatalf("Load missing should return empty manifest, got error: %v", err)
	}
	if m.SourceCount() != 0 {
		t.Error("expected empty manifest")
	}
}

func TestMarkCompiled(t *testing.T) {
	m := New()
	m.AddSource("raw/a.md", "sha256:xyz", "article", 500)
	m.MarkCompiled("raw/a.md", "wiki/summaries/a.md", []string{"attention"})

	s := m.Sources["raw/a.md"]
	if s.Status != "compiled" {
		t.Errorf("expected compiled, got %q", s.Status)
	}
	if s.SummaryPath != "wiki/summaries/a.md" {
		t.Errorf("expected summary path, got %q", s.SummaryPath)
	}
}

func TestPendingSources(t *testing.T) {
	m := New()
	m.AddSource("raw/a.md", "h1", "article", 100)
	m.AddSource("raw/b.md", "h2", "article", 200)
	m.MarkCompiled("raw/a.md", "wiki/summaries/a.md", nil)

	pending := m.PendingSources()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}
	if _, ok := pending["raw/b.md"]; !ok {
		t.Error("expected raw/b.md to be pending")
	}
}

func TestRemoveSource(t *testing.T) {
	m := New()
	m.AddSource("raw/a.md", "h1", "article", 100)
	m.RemoveSource("raw/a.md")
	if m.SourceCount() != 0 {
		t.Error("expected 0 after remove")
	}
}

func TestArticlesFromSource(t *testing.T) {
	m := New()
	m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf", "raw/notes.md"})
	m.AddConcept("transformer", "wiki/concepts/transformer.md", []string{"raw/paper.pdf"})
	m.AddConcept("lstm", "wiki/concepts/lstm.md", []string{"raw/rnn-book.pdf"})

	// Multi-concept source
	articles := m.ArticlesFromSource("raw/paper.pdf")
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles from paper.pdf, got %d", len(articles))
	}

	// Single-concept source
	articles = m.ArticlesFromSource("raw/rnn-book.pdf")
	if len(articles) != 1 || articles[0] != "lstm" {
		t.Errorf("expected [lstm], got %v", articles)
	}

	// Nonexistent source
	articles = m.ArticlesFromSource("raw/nonexistent.pdf")
	if len(articles) != 0 {
		t.Errorf("expected empty, got %v", articles)
	}

	// Empty manifest
	empty := New()
	articles = empty.ArticlesFromSource("raw/anything.md")
	if len(articles) != 0 {
		t.Errorf("expected empty from empty manifest, got %v", articles)
	}
}

func TestCascadeRemoveSource_SingleSource(t *testing.T) {
	m := New()
	m.AddSource("raw/paper.pdf", "h1", "paper", 5000)
	m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf"})

	// Simulate cascade: look up affected concepts BEFORE removing source
	affected := m.ArticlesFromSource("raw/paper.pdf")
	if len(affected) != 1 || affected[0] != "attention" {
		t.Fatalf("expected [attention], got %v", affected)
	}

	// Single-source concept → orphaned (in real pipeline, would warn + optionally prune)
	concept := m.Concepts["attention"]
	if len(concept.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(concept.Sources))
	}

	// Simulate prune: delete concept
	delete(m.Concepts, "attention")
	m.RemoveSource("raw/paper.pdf")

	if m.ConceptCount() != 0 {
		t.Errorf("expected 0 concepts after prune, got %d", m.ConceptCount())
	}
	if m.SourceCount() != 0 {
		t.Errorf("expected 0 sources after remove, got %d", m.SourceCount())
	}
}

func TestCascadeRemoveSource_MultiSource(t *testing.T) {
	m := New()
	m.AddSource("raw/paper.pdf", "h1", "paper", 5000)
	m.AddSource("raw/notes.md", "h2", "article", 1000)
	m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf", "raw/notes.md"})

	// Simulate cascade: multi-source → update sources list
	concept := m.Concepts["attention"]
	var updated []string
	for _, s := range concept.Sources {
		if s != "raw/paper.pdf" {
			updated = append(updated, s)
		}
	}
	concept.Sources = updated
	m.Concepts["attention"] = concept

	m.RemoveSource("raw/paper.pdf")

	// Concept survives with updated sources
	if m.ConceptCount() != 1 {
		t.Errorf("expected 1 concept (survived), got %d", m.ConceptCount())
	}
	c := m.Concepts["attention"]
	if len(c.Sources) != 1 || c.Sources[0] != "raw/notes.md" {
		t.Errorf("expected [raw/notes.md], got %v", c.Sources)
	}
}

func TestCascadeRemoveSource_NoOrphans(t *testing.T) {
	m := New()
	m.AddSource("raw/paper.pdf", "h1", "paper", 5000)
	m.AddConcept("lstm", "wiki/concepts/lstm.md", []string{"raw/other.pdf"})

	// Remove a source that doesn't affect any concept
	affected := m.ArticlesFromSource("raw/paper.pdf")
	if len(affected) != 0 {
		t.Errorf("expected 0 affected, got %d", len(affected))
	}

	m.RemoveSource("raw/paper.pdf")
	if m.ConceptCount() != 1 {
		t.Errorf("expected 1 concept unchanged, got %d", m.ConceptCount())
	}
}

func TestSourcesForArticle(t *testing.T) {
	m := New()
	m.AddConcept("attention", "wiki/concepts/attention.md", []string{"raw/paper.pdf", "raw/notes.md"})

	sources := m.SourcesForArticle("attention")
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	// Nonexistent concept
	sources = m.SourcesForArticle("nonexistent")
	if sources != nil {
		t.Errorf("expected nil for nonexistent, got %v", sources)
	}
}
