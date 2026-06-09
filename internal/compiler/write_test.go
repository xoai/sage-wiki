package compiler

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestStore(t *testing.T) *ontology.Store {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ontology.NewStore(db, nil, nil)
}

func TestExtractRelations_SameBlockCreatesRelation(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention implements [[self-attention]] for optimization."
	extractRelations("flash-attention", content, store, patterns)

	relations, err := store.ListRelations("", 100)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(relations) != 1 {
		t.Fatalf("expected 1 relation, got %d", len(relations))
	}
	r := relations[0]
	if r.SourceID != "flash-attention" || r.TargetID != "self-attention" || r.Relation != "implements" {
		t.Errorf("unexpected relation: %s -[%s]-> %s", r.SourceID, r.Relation, r.TargetID)
	}
}

func TestExtractRelations_DifferentBlockNoRelation(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention is useful.\n\nIt implements optimization.\n\nSee [[self-attention]] for details."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 0 {
		t.Errorf("expected 0 relations (cross-block), got %d", len(relations))
	}
}

func TestExtractRelations_SingleParagraph(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention implements [[self-attention]] efficiently."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 relation for single paragraph, got %d", len(relations))
	}
}

func TestExtractRelations_SelfLinkSkipped(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "Flash attention [[flash-attention]] implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Fatalf("expected 1 relation (self-link skipped), got %d", len(relations))
	}
	if relations[0].TargetID != "self-attention" {
		t.Errorf("TargetID = %q, want self-attention", relations[0].TargetID)
	}
}

func TestExtractRelations_MultipleWikilinksSameTarget(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements"},
	}

	content := "[[self-attention]] and also [[self-attention]] implements optimization."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 relation (deduplicated), got %d", len(relations))
	}
}

func TestExtractRelations_ValidSourcesFilters(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	content := "Flash attention implements [[self-attention]]."

	t.Run("excluded source type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidSources: []string{"concept"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 0 {
			t.Errorf("expected 0 (technique not in ValidSources [concept]), got %d", len(relations))
		}
	})

	t.Run("included source type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidSources: []string{"technique", "concept"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 1 {
			t.Errorf("expected 1 (technique in ValidSources), got %d", len(relations))
		}
	})
}

func TestExtractRelations_ValidTargetsFilters(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	content := "Flash attention implements [[self-attention]]."

	t.Run("excluded target type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"technique"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 0 {
			t.Errorf("expected 0 (concept not in ValidTargets), got %d", len(relations))
		}
	})

	t.Run("included target type", func(t *testing.T) {
		store2 := setupTestStore(t)
		store2.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		store2.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

		patterns := []ontology.RelationPattern{
			{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"concept", "technique"}},
		}
		extractRelations("flash-attention", content, store2, patterns)
		relations, _ := store2.ListRelations("", 100)
		if len(relations) != 1 {
			t.Errorf("expected 1 (concept in ValidTargets), got %d", len(relations))
		}
	})
}

func TestExtractRelations_EmptyValidFiltersAllowsAll(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
	store.AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements", ValidSources: nil, ValidTargets: nil},
	}

	content := "Flash attention implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 1 {
		t.Errorf("expected 1 (nil filters allow all), got %d", len(relations))
	}
}

func TestExtractRelations_EntityNotFoundWithValidTargets(t *testing.T) {
	store := setupTestStore(t)

	store.AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})

	patterns := []ontology.RelationPattern{
		{Keywords: []string{"implements"}, Relation: "implements", ValidTargets: []string{"concept"}},
	}

	content := "Flash attention implements [[self-attention]]."
	extractRelations("flash-attention", content, store, patterns)

	relations, _ := store.ListRelations("", 100)
	if len(relations) != 0 {
		t.Errorf("expected 0 (unknown target type '' not in ValidTargets), got %d", len(relations))
	}
}

func TestBuildFrontmatter_EmitsEntityType(t *testing.T) {
	concept := ExtractedConcept{
		Name:    "flash-attention",
		Aliases: []string{"fa"},
		Sources: []string{"raw/transformer.md"},
	}
	fields := map[string]string{"confidence": "high"}

	got := buildFrontmatter(concept, "technique", fields, nil, time.UTC)

	if !strings.Contains(got, "concept: flash-attention") {
		t.Error("missing concept field")
	}
	if !strings.Contains(got, "entity_type: technique") {
		t.Errorf("missing entity_type field; got:\n%s", got)
	}
	if !strings.Contains(got, "aliases:") {
		t.Error("missing aliases field")
	}
	if !strings.Contains(got, "sources:") {
		t.Error("missing sources field")
	}
	if !strings.Contains(got, "confidence: high") {
		t.Error("missing confidence field")
	}

	// Verify field order: concept → entity_type → aliases → sources → confidence
	conceptIdx := strings.Index(got, "concept:")
	entityIdx := strings.Index(got, "entity_type:")
	aliasIdx := strings.Index(got, "aliases:")
	srcIdx := strings.Index(got, "sources:")
	confIdx := strings.Index(got, "confidence:")
	if !(conceptIdx < entityIdx && entityIdx < aliasIdx && aliasIdx < srcIdx && srcIdx < confIdx) {
		t.Errorf("frontmatter field order incorrect:\n%s", got)
	}
}

func TestBuildFrontmatter_FallbackEntityType(t *testing.T) {
	concept := ExtractedConcept{
		Name:    "some-concept",
		Aliases: nil,
		Sources: nil,
	}
	fields := map[string]string{}

	// Caller passes "concept" as the resolved fallback for empty/invalid types
	got := buildFrontmatter(concept, "concept", fields, nil, time.UTC)

	if !strings.Contains(got, "entity_type: concept") {
		t.Errorf("expected entity_type: concept (fallback); got:\n%s", got)
	}
}

func TestStripOuterCodeFence(t *testing.T) {
	// Whole-body wrap with info string → unwrapped.
	wrapped := "```markdown\n# Title\n\nBody text with [[link]].\n```"
	got := stripOuterCodeFence(wrapped)
	if strings.HasPrefix(strings.TrimSpace(got), "```") {
		t.Errorf("wrapper not stripped: %q", got)
	}
	if !strings.Contains(got, "# Title") || !strings.Contains(got, "[[link]]") {
		t.Errorf("body content lost: %q", got)
	}

	// Embedded code block only (does not start with fence) → untouched.
	embedded := "# Title\n\n```go\ncode\n```\n\nmore"
	if stripOuterCodeFence(embedded) != embedded {
		t.Error("article with embedded code block should be untouched")
	}

	// Article that opens AND closes with two SEPARATE code blocks (4 fences)
	// → untouched (must not corrupt code).
	twoBlocks := "```go\na\n```\ntext\n```py\nb\n```"
	if stripOuterCodeFence(twoBlocks) != twoBlocks {
		t.Error("article with two code blocks must not be stripped")
	}

	// No fence → identity.
	plain := "# Title\n\nplain body"
	if stripOuterCodeFence(plain) != plain {
		t.Error("plain article should be untouched")
	}
}

func TestStripAntiPatternSentences(t *testing.T) {
	phrases := []string{"this article will", "综上所述"}

	// English filler sentence dropped, neighbour kept.
	in := "Attention weighs tokens. This article will explain attention. It is fast."
	got := stripAntiPatternSentences(in, phrases)
	if strings.Contains(got, "This article will") {
		t.Errorf("anti-pattern sentence not dropped: %q", got)
	}
	if !strings.Contains(got, "Attention weighs tokens.") || !strings.Contains(got, "It is fast.") {
		t.Errorf("non-matching sentences lost: %q", got)
	}

	// Chinese filler sentence dropped on 。 boundary.
	zh := "自注意力很有用。综上所述这是总结。模型很快。"
	gotZh := stripAntiPatternSentences(zh, phrases)
	if strings.Contains(gotZh, "综上所述") {
		t.Errorf("Chinese anti-pattern not dropped: %q", gotZh)
	}
	if !strings.Contains(gotZh, "自注意力很有用") || !strings.Contains(gotZh, "模型很快") {
		t.Errorf("Chinese neighbours lost: %q", gotZh)
	}

	// Empty/nil phrases → identity.
	if stripAntiPatternSentences(in, nil) != in {
		t.Error("nil phrases should be identity")
	}
	if stripAntiPatternSentences(in, []string{}) != in {
		t.Error("empty phrases should be identity")
	}

	// Never-empty guard: every sentence matches → return original.
	allMatch := "This article will start. This article will end."
	if got := stripAntiPatternSentences(allMatch, phrases); got != allMatch {
		t.Errorf("never-empty guard failed: %q", got)
	}

	// Fenced code region is skipped (phrase inside code survives).
	withCode := "Intro.\n```\nthis article will stay in code\n```\nThis article will go."
	gotCode := stripAntiPatternSentences(withCode, phrases)
	if !strings.Contains(gotCode, "this article will stay in code") {
		t.Errorf("fenced code should be untouched: %q", gotCode)
	}
	if strings.Contains(gotCode, "This article will go") {
		t.Errorf("prose anti-pattern outside code should be dropped: %q", gotCode)
	}
}

func TestSanitizeWikilinks(t *testing.T) {
	aliasMap := map[string]string{
		"中文概念":            "chinese-concept",
		"Attention":       "attention",
		"chinese-concept": "chinese-concept", // canonical maps to itself
	}

	// Alias rewritten to canonical id.
	got := sanitizeWikilinks("see [[中文概念]] here", aliasMap)
	if got != "see [[chinese-concept]] here" {
		t.Errorf("alias not rewritten: %q", got)
	}

	// Already-canonical link unchanged (maps to itself → no-op).
	same := "see [[chinese-concept]]"
	if sanitizeWikilinks(same, aliasMap) != same {
		t.Errorf("canonical link should be unchanged: %q", sanitizeWikilinks(same, aliasMap))
	}

	// Unknown target falls through unchanged.
	unknown := "see [[ghost-concept]]"
	if sanitizeWikilinks(unknown, aliasMap) != unknown {
		t.Errorf("unknown link should be unchanged: %q", sanitizeWikilinks(unknown, aliasMap))
	}

	// Piped link: target resolved, display preserved.
	piped := sanitizeWikilinks("see [[中文概念|the concept]]", aliasMap)
	if piped != "see [[chinese-concept|the concept]]" {
		t.Errorf("piped link target+display handling wrong: %q", piped)
	}

	// nil map → identity.
	if sanitizeWikilinks(unknown, nil) != unknown {
		t.Error("nil map should be identity")
	}
}

func TestBuildAliasMap(t *testing.T) {
	concepts := []ExtractedConcept{
		{Name: "attention", Aliases: []string{"自注意力", "self-attention"}},
		{Name: "flash-attention", Aliases: []string{"闪光注意力"}},
	}
	m := buildAliasMap(concepts)
	if m["自注意力"] != "attention" {
		t.Errorf("alias not mapped: %v", m["自注意力"])
	}
	if m["闪光注意力"] != "flash-attention" {
		t.Errorf("alias not mapped: %v", m["闪光注意力"])
	}
	// Display-form maps to id.
	if m["Attention"] != "attention" {
		t.Errorf("display form not mapped: %v", m["Attention"])
	}
}

// TestStripAntiPatternSentences_PeriodContentSafe is defense-in-depth for the
// spec-review Critical: even if a frontmatter-like line with periods (source
// paths) reached the splitter, non-matching content must survive byte-for-byte.
// In production the body processors run BEFORE buildFrontmatter, so frontmatter
// never reaches them — this guards the splitter itself against mangling.
func TestStripAntiPatternSentences_PeriodContentSafe(t *testing.T) {
	phrases := []string{"this article will", "综上所述"}
	line := `sources: ["raw/a.md", "raw/b.md", "dir/c.ext.md"]`
	if got := stripAntiPatternSentences(line, phrases); got != line {
		t.Errorf("period-containing non-matching content mangled:\n in: %q\nout: %q", line, got)
	}
}

// TestBuildAliasMap_CanonicalWinsOverAliasCollision covers the review Major:
// a real concept's own id must beat another concept's colliding alias.
func TestBuildAliasMap_CanonicalWinsOverAliasCollision(t *testing.T) {
	concepts := []ExtractedConcept{
		{Name: "transformer", Aliases: []string{"attention"}}, // aliases the OTHER concept's id
		{Name: "attention", Aliases: []string{"self-attention"}},
	}
	m := buildAliasMap(concepts)
	if m["attention"] != "attention" {
		t.Errorf("canonical id must win over colliding alias: m[attention]=%q, want \"attention\"", m["attention"])
	}
	if m["self-attention"] != "attention" {
		t.Errorf("non-colliding alias should still map: %q", m["self-attention"])
	}
}

// TestStripAntiPatternSentences_UnclosedFence documents the fail-safe: an
// unclosed ``` leaves subsequent lines treated as code (under-strip), never
// corrupting them.
func TestStripAntiPatternSentences_UnclosedFence(t *testing.T) {
	phrases := []string{"this article will"}
	in := "Intro.\n```\nthis article will survive (unclosed fence)."
	got := stripAntiPatternSentences(in, phrases)
	if !strings.Contains(got, "this article will survive") {
		t.Errorf("content after unclosed fence should be left intact: %q", got)
	}
}
