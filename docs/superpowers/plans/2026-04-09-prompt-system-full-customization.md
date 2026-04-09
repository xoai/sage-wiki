# Prompt System Full Customization — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make all 3 compiler passes (summarize/extract/write) use the template system, add config-driven content type detection and ontology relation types, create domain-specific Chinese prompt templates.

**Architecture:** Config-driven approach — `config.yaml` gains `type_signals` and `ontology` sections. `DetectSourceType()` gains content-aware matching. `buildArticlePrompt()` and concept extraction switch from hardcoded strings to `prompts.Render()`. SQLite CHECK constraint replaced by application-layer validation.

**Tech Stack:** Go 1.22+, SQLite, Go `text/template`, YAML config

---

## File Map

### Create
| File | Responsibility |
|------|---------------|
| `internal/extract/readhead.go` | `ReadHead()` helper — reads first N runes from a file |
| `internal/ontology/relation_config.go` | `RelationTypeDef`, `ValidRelation()`, `NormalizeRelation()`, `DefaultRelationTypeDefs()` |
| `internal/ontology/relation_config_test.go` | Tests for validation/normalization |
| `internal/config/config_test.go` | Additional tests for TypeSignal/Ontology parsing (append to existing) |
| `~/claude-workspace/wiki/prompts/summarize-regulation.md` | Regulation-specific summarize template |
| `~/claude-workspace/wiki/prompts/summarize-research.md` | Research report summarize template |
| `~/claude-workspace/wiki/prompts/summarize-interview.md` | Expert interview summarize template |
| `~/claude-workspace/wiki/prompts/summarize-announcement.md` | Transaction announcement summarize template |

### Modify
| File | Lines | What changes |
|------|-------|-------------|
| `internal/config/config.go` | 13-28, 63-75, 94-121, 163-193 | Add `TypeSignal`, `RelationTypeDef`, `OntologyConfig` structs; add fields to `Config`; update `Defaults()` and `Validate()` |
| `internal/extract/extract.go` | 261-289 | `DetectSourceType()` gains `contentHead` + `typeSignals` params; add `matchesSignal()` |
| `internal/extract/extract_test.go` | 166-191 | Update existing tests for new signature; add signal-matching tests |
| `internal/compiler/diff.go` | 70-76 | Pass `contentHead` + `cfg.TypeSignals` to `DetectSourceType()` |
| `internal/compiler/pipeline.go` | 850-852 | `extractType()` gains config param, reads contentHead |
| `internal/compiler/pipeline.go` | 289-292, 677-688 | Call sites of `extractType()` pass config |
| `internal/wiki/ingest.go` | 114 | Pass `contentHead` + `cfg.TypeSignals` to `DetectSourceType()` |
| `internal/compiler/write.go` | 198-244 | `buildArticlePrompt()` → `prompts.Render("write_article", ...)` with legacy fallback |
| `internal/compiler/concepts.go` | 82-107 | Inline prompt → `prompts.Render("extract_concepts", ...)` with legacy fallback |
| `internal/ontology/ontology.go` | 20-29 | Consts become vars; keep for backward compat |
| `internal/storage/db.go` | 191-202 | Remove CHECK constraint on `relation`; add migration function |
| `internal/compiler/write.go` | 290-337 | `extractRelations()` reads relation types from config |
| `~/claude-workspace/wiki/config.yaml` | bottom | Add `type_signals` and `ontology` sections |

---

### Task 1: Config structs and parsing (G7)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write test for TypeSignal parsing**

Append to `internal/config/config_test.go`:

```go
func TestTypeSignalParsing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
version: 1
project: test-wiki
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
type_signals:
  - type: regulation
    filename_keywords: ["法规", "办法"]
    content_keywords: ["第一条", "第二条"]
    min_content_hits: 2
  - type: research
    filename_keywords: ["研报"]
    content_keywords: ["投资评级"]
    min_content_hits: 1
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.TypeSignals) != 2 {
		t.Fatalf("expected 2 type signals, got %d", len(cfg.TypeSignals))
	}
	if cfg.TypeSignals[0].Type != "regulation" {
		t.Errorf("expected type 'regulation', got %q", cfg.TypeSignals[0].Type)
	}
	if len(cfg.TypeSignals[0].FilenameKeywords) != 2 {
		t.Errorf("expected 2 filename keywords, got %d", len(cfg.TypeSignals[0].FilenameKeywords))
	}
	if cfg.TypeSignals[0].MinContentHits != 2 {
		t.Errorf("expected min_content_hits 2, got %d", cfg.TypeSignals[0].MinContentHits)
	}
}

func TestOntologyConfigParsing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
version: 1
project: test-wiki
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
ontology:
  max_relation_types: 20
  max_content_types: 10
  relation_types:
    - name: implements
      synonyms: ["实现了", "implementation of"]
    - name: amends
      synonyms: ["修订", "废止"]
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Ontology == nil {
		t.Fatal("expected ontology config")
	}
	if cfg.Ontology.MaxRelationTypes != 20 {
		t.Errorf("expected max 20, got %d", cfg.Ontology.MaxRelationTypes)
	}
	if len(cfg.Ontology.RelationTypes) != 2 {
		t.Fatalf("expected 2 relation types, got %d", len(cfg.Ontology.RelationTypes))
	}
	if cfg.Ontology.RelationTypes[1].Name != "amends" {
		t.Errorf("expected 'amends', got %q", cfg.Ontology.RelationTypes[1].Name)
	}
}

func TestValidateOntologyLimits(t *testing.T) {
	cfg := Defaults()
	cfg.Project = "test"

	// Build 21 relation types to exceed limit of 20
	rels := make([]RelationTypeDef, 21)
	for i := range rels {
		rels[i] = RelationTypeDef{Name: fmt.Sprintf("rel_%d", i)}
	}
	cfg.Ontology = &OntologyConfig{
		MaxRelationTypes: 20,
		RelationTypes:    rels,
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for exceeding max_relation_types")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/config/ -run TestTypeSignalParsing -v`
Expected: FAIL — `TypeSignal` type not defined

- [ ] **Step 3: Add structs and fields to config.go**

Add these types after the `ServeConfig` struct (after line 91):

```go
// TypeSignal defines keywords for content-type detection.
type TypeSignal struct {
	Type             string   `yaml:"type"`
	FilenameKeywords []string `yaml:"filename_keywords"`
	ContentKeywords  []string `yaml:"content_keywords"`
	MinContentHits   int      `yaml:"min_content_hits"`
}

// RelationTypeDef defines an ontology relation type with synonyms.
type RelationTypeDef struct {
	Name     string   `yaml:"name"`
	Synonyms []string `yaml:"synonyms,omitempty"`
}

// OntologyConfig defines configurable ontology settings.
type OntologyConfig struct {
	RelationTypes    []RelationTypeDef `yaml:"relation_types"`
	MaxRelationTypes int               `yaml:"max_relation_types"`
	MaxContentTypes  int               `yaml:"max_content_types"`
}
```

Add fields to `Config` struct (after `Serve` field):

```go
	TypeSignals []TypeSignal    `yaml:"type_signals,omitempty"`
	Ontology    *OntologyConfig `yaml:"ontology,omitempty"`
```

Add validation in `Validate()` (before the final `return nil`):

```go
	// Validate ontology limits
	if c.Ontology != nil {
		maxRel := c.Ontology.MaxRelationTypes
		if maxRel <= 0 {
			maxRel = 20 // default
		}
		if len(c.Ontology.RelationTypes) > maxRel {
			return fmt.Errorf("config: relation_types count %d exceeds max_relation_types %d",
				len(c.Ontology.RelationTypes), maxRel)
		}
		maxCt := c.Ontology.MaxContentTypes
		if maxCt <= 0 {
			maxCt = 10 // default
		}
		if len(c.TypeSignals) > maxCt {
			return fmt.Errorf("config: type_signals count %d exceeds max_content_types %d",
				len(c.TypeSignals), maxCt)
		}
	}
```

Add `"fmt"` to the test file imports if not present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/config/ -v`
Expected: ALL PASS

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 6: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add TypeSignal and OntologyConfig to config schema

Add config structs for content-type detection signals (filename/content
keyword matching) and configurable ontology relation types with synonyms.
Includes validation for max limits."
```

---

### Task 2: ReadHead helper (G2 prerequisite)

**Files:**
- Create: `internal/extract/readhead.go`

- [ ] **Step 1: Write test**

Create test at end of `internal/extract/extract_test.go`:

```go
func TestReadHead(t *testing.T) {
	dir := t.TempDir()

	// ASCII file
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("Hello, World! This is a test file with some content."), 0644)
	got := ReadHead(path, 5)
	if got != "Hello" {
		t.Errorf("ReadHead(5) = %q, want %q", got, "Hello")
	}

	// Chinese content
	cnPath := filepath.Join(dir, "chinese.txt")
	os.WriteFile(cnPath, []byte("第一条 为了规范证券发行"), 0644)
	got = ReadHead(cnPath, 10)
	if len([]rune(got)) > 10 {
		t.Errorf("ReadHead(10) returned %d runes, want <= 10", len([]rune(got)))
	}

	// Non-existent file
	got = ReadHead("/nonexistent/file.txt", 100)
	if got != "" {
		t.Errorf("ReadHead(nonexistent) = %q, want empty", got)
	}

	// File shorter than limit
	got = ReadHead(path, 10000)
	if got != "Hello, World! This is a test file with some content." {
		t.Errorf("ReadHead(10000) = %q, want full content", got)
	}
}
```

Add `"os"` and `"path/filepath"` to test imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/extract/ -run TestReadHead -v`
Expected: FAIL — `ReadHead` undefined

- [ ] **Step 3: Implement ReadHead**

Create `internal/extract/readhead.go`:

```go
package extract

import (
	"os"
	"unicode/utf8"
)

// ReadHead reads the first maxRunes runes from a file.
// Returns empty string on error or if file doesn't exist.
func ReadHead(path string, maxRunes int) string {
	// Read more bytes than needed to handle multi-byte runes
	maxBytes := maxRunes * 4
	if maxBytes > 8192 {
		maxBytes = 8192
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, maxBytes)
	n, _ := f.Read(buf)
	buf = buf[:n]

	// Count runes and truncate
	runes := 0
	i := 0
	for i < len(buf) && runes < maxRunes {
		_, size := utf8.DecodeRune(buf[i:])
		i += size
		runes++
	}

	return string(buf[:i])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/extract/ -run TestReadHead -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/extract/readhead.go internal/extract/extract_test.go
git commit -m "feat: add ReadHead helper for content-aware type detection

Reads first N runes from a file for keyword matching.
Handles multi-byte UTF-8 correctly for Chinese content."
```

---

### Task 3: DetectSourceType content-aware upgrade (G1 + G1b)

**Files:**
- Modify: `internal/extract/extract.go:261-289`
- Modify: `internal/extract/extract_test.go:166-191`

- [ ] **Step 1: Write tests for new signature**

Replace the existing `TestDetectSourceType` function and add signal tests in `internal/extract/extract_test.go`:

```go
func TestDetectSourceType(t *testing.T) {
	// Backward compat: nil signals = extension-only
	tests := []struct {
		path     string
		expected string
	}{
		{"paper.pdf", "paper"},
		{"article.md", "article"},
		{"notes.txt", "article"},
		{"main.go", "code"},
		{"script.py", "code"},
		{"data.csv", "dataset"},
		{"report.docx", "article"},
		{"slides.pptx", "article"},
		{"data.xlsx", "dataset"},
		{"book.epub", "article"},
		{"mail.eml", "article"},
		{"output.log", "article"},
		{"transcript.vtt", "article"},
	}
	for _, tt := range tests {
		got := DetectSourceType(tt.path, "", nil)
		if got != tt.expected {
			t.Errorf("DetectSourceType(%s, \"\", nil) = %s, want %s", tt.path, got, tt.expected)
		}
	}
}

func TestDetectSourceTypeWithSignals(t *testing.T) {
	signals := []config.TypeSignal{
		{
			Type:             "regulation",
			FilenameKeywords: []string{"法规", "办法"},
			ContentKeywords:  []string{"第一条", "第二条", "为了规范"},
			MinContentHits:   2,
		},
		{
			Type:             "research",
			FilenameKeywords: []string{"研报"},
			ContentKeywords:  []string{"投资评级", "目标价"},
			MinContentHits:   1,
		},
	}

	tests := []struct {
		name        string
		path        string
		contentHead string
		expected    string
	}{
		{"filename match", "/path/证券法规汇编.pdf", "", "regulation"},
		{"content match", "/path/document.pdf", "第一条 为了规范证券市场 第二条 适用范围", "regulation"},
		{"content below threshold", "/path/doc.pdf", "第一条 只有一个关键词", "paper"},
		{"research filename", "/path/AI研报.pdf", "", "research"},
		{"research content", "/path/report.pdf", "本报告投资评级为买入", "research"},
		{"no match fallback pdf", "/path/random.pdf", "no keywords here", "paper"},
		{"no match fallback md", "/path/notes.md", "no keywords here", "article"},
		{"signal priority", "/path/法规研报.pdf", "", "regulation"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectSourceType(tt.path, tt.contentHead, signals)
			if got != tt.expected {
				t.Errorf("DetectSourceType(%s) = %s, want %s", tt.name, got, tt.expected)
			}
		})
	}
}
```

Add `"github.com/xoai/sage-wiki/internal/config"` to test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/extract/ -run TestDetectSourceType -v`
Expected: FAIL — signature mismatch (too many arguments)

- [ ] **Step 3: Update DetectSourceType**

In `internal/extract/extract.go`, replace the function starting at line 261:

```go
// DetectSourceType detects the source type from file path, optional content
// head text, and optional config-driven type signals.
// When typeSignals is nil or empty, falls back to extension-only detection.
func DetectSourceType(path string, contentHead string, typeSignals []config.TypeSignal) string {
	// Try config-driven signals first
	for _, sig := range typeSignals {
		if matchesSignal(path, contentHead, sig) {
			return sig.Type
		}
	}

	// Fallback to extension-based detection
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "paper"
	case ".md", ".txt":
		return "article"
	case ".docx", ".doc":
		return "article"
	case ".xlsx", ".xls", ".csv":
		return "dataset"
	case ".pptx", ".ppt":
		return "article"
	case ".epub":
		return "article"
	case ".eml", ".msg", ".mbox":
		return "article"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		return "image"
	case ".log", ".vtt", ".srt":
		return "article"
	default:
		if isCodeFile(ext) {
			return "code"
		}
		return "article"
	}
}

// matchesSignal checks if a file matches a type signal by filename keywords
// or content keywords.
func matchesSignal(path, contentHead string, sig config.TypeSignal) bool {
	filenameLower := strings.ToLower(filepath.Base(path))

	// Filename keyword match — any one keyword is enough
	for _, kw := range sig.FilenameKeywords {
		if strings.Contains(filenameLower, strings.ToLower(kw)) {
			return true
		}
	}

	// Content keyword match — must hit MinContentHits threshold
	if contentHead != "" && sig.MinContentHits > 0 {
		contentLower := strings.ToLower(contentHead)
		hits := 0
		for _, kw := range sig.ContentKeywords {
			if strings.Contains(contentLower, strings.ToLower(kw)) {
				hits++
			}
		}
		if hits >= sig.MinContentHits {
			return true
		}
	}

	return false
}
```

Add `"github.com/xoai/sage-wiki/internal/config"` to the extract.go imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/extract/ -v`
Expected: ALL PASS

- [ ] **Step 5: Fix compilation errors in callers**

The 3 call sites will now fail to compile because they pass 1 arg instead of 3. Temporarily fix them to pass empty string and nil:

In `internal/compiler/diff.go` line 73, change:
```go
Type: extract.DetectSourceType(path),
```
to:
```go
Type: extract.DetectSourceType(path, "", nil),
```

In `internal/compiler/pipeline.go` line 851, change:
```go
return extract.DetectSourceType(path)
```
to:
```go
return extract.DetectSourceType(path, "", nil)
```

In `internal/wiki/ingest.go` line 114, change:
```go
srcType := extract.DetectSourceType(absPath)
```
to:
```go
srcType := extract.DetectSourceType(absPath, "", nil)
```

- [ ] **Step 6: Verify build and full test suite**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 7: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/extract/extract.go internal/extract/extract_test.go \
  internal/compiler/diff.go internal/compiler/pipeline.go internal/wiki/ingest.go
git commit -m "feat: make DetectSourceType content-aware with configurable signals

DetectSourceType now accepts contentHead and typeSignals parameters.
Config-driven signals match by filename keywords or content keywords
with a configurable hit threshold. Falls back to extension-only
detection when no signals are configured.

Callers temporarily pass nil signals — Task 4 will thread config through."
```

---

### Task 4: Thread config through DetectSourceType callers (G2)

**Files:**
- Modify: `internal/compiler/diff.go:70-76`
- Modify: `internal/compiler/pipeline.go:850-852`
- Modify: `internal/wiki/ingest.go:114`

- [ ] **Step 1: Update diff.go to read content head and pass config**

In `internal/compiler/diff.go`, in the `WalkDir` callback around line 70-76, change:

```go
			current[relPath] = SourceInfo{
				Path: relPath,
				Hash: hash,
				Type: extract.DetectSourceType(path, "", nil),
				Size: info.Size(),
			}
```
to:
```go
			contentHead := extract.ReadHead(path, 500)
			current[relPath] = SourceInfo{
				Path: relPath,
				Hash: hash,
				Type: extract.DetectSourceType(path, contentHead, cfg.TypeSignals),
				Size: info.Size(),
			}
```

- [ ] **Step 2: Update pipeline.go extractType() to accept config**

In `internal/compiler/pipeline.go`, change the `extractType` function (line 850-852):

```go
func extractType(path string) string {
	return extract.DetectSourceType(path, "", nil)
}
```
to:
```go
func extractType(path string, typeSignals []config.TypeSignal) string {
	contentHead := extract.ReadHead(path, 500)
	return extract.DetectSourceType(path, contentHead, typeSignals)
}
```

Then update all 3 call sites of `extractType()` in pipeline.go:

Line ~291: `Tags: []string{extractType(sr.SourcePath)},` → `Tags: []string{extractType(sr.SourcePath, state.Config.TypeSignals)},`

Line ~679: `mf.AddSource(br.CustomID, "", extractType(br.CustomID), 0)` → `mf.AddSource(br.CustomID, "", extractType(br.CustomID, state.Config.TypeSignals), 0)`

Line ~687: `Tags: []string{extractType(br.CustomID)},` → `Tags: []string{extractType(br.CustomID, state.Config.TypeSignals)},`

Verify that `state.Config` is accessible — check the `CompileState` struct for a `Config` field. If it has `*config.Config`, use `state.Config.TypeSignals`. If not, check how `cfg` is passed in the `Compile()` function and thread it through.

Add `"github.com/xoai/sage-wiki/internal/config"` to pipeline.go imports if not present.

- [ ] **Step 3: Update ingest.go**

In `internal/wiki/ingest.go` line 114, change:

```go
srcType := extract.DetectSourceType(absPath, "", nil)
```
to:
```go
contentHead := extract.ReadHead(absPath, 500)
srcType := extract.DetectSourceType(absPath, contentHead, cfg.TypeSignals)
```

Verify `cfg` is accessible in this function scope (it's passed as `*config.Config` parameter).

- [ ] **Step 4: Build and test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 5: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/compiler/diff.go internal/compiler/pipeline.go internal/wiki/ingest.go
git commit -m "feat: thread config type signals through all DetectSourceType callers

All 3 call sites (diff.go, pipeline.go, ingest.go) now read the first
500 chars of each file and pass config-driven type signals to
DetectSourceType for content-aware type detection."
```

---

### Task 5: write.go switch to template system (G3)

**Files:**
- Modify: `internal/compiler/write.go:198-244`

- [ ] **Step 1: Rename current function as legacy fallback**

In `internal/compiler/write.go`, rename the existing `buildArticlePrompt` function (line 198):

```go
func buildArticlePromptLegacy(concept ExtractedConcept, existing string, related []string) string {
```

(Keep the entire body unchanged.)

- [ ] **Step 2: Add new template-based buildArticlePrompt**

Add this new function before the legacy one:

```go
func buildArticlePrompt(concept ExtractedConcept, existing string, related []string) string {
	prompt, err := prompts.Render("write_article", prompts.WriteArticleData{
		ConceptName:     formatConceptName(concept.Name),
		ConceptID:       concept.Name,
		Sources:         strings.Join(concept.Sources, ", "),
		RelatedConcepts: related,
		ExistingArticle: existing,
		Learnings:       "", // learnings integration is future work
		Aliases:         strings.Join(concept.Aliases, ", "),
		SourceList:      strings.Join(quoteYAMLList(concept.Sources), ", "),
		RelatedList:     strings.Join(related, ", "),
	})
	if err != nil {
		log.Warn("template render failed, using legacy prompt", "error", err)
		return buildArticlePromptLegacy(concept, existing, related)
	}
	return prompt
}
```

Add `"github.com/xoai/sage-wiki/internal/prompts"` to write.go imports.

- [ ] **Step 3: Build and test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass. Call site at line 100 (`prompt := buildArticlePrompt(...)`) doesn't change — same signature.

- [ ] **Step 4: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/compiler/write.go
git commit -m "feat: switch buildArticlePrompt to use prompts.Render template system

The write_article template (from prompts/ directory or embedded default)
is now used for article generation. Falls back to the legacy hardcoded
prompt if template rendering fails."
```

---

### Task 6: concepts.go switch to template system (G4)

**Files:**
- Modify: `internal/compiler/concepts.go:82-107`

- [ ] **Step 1: Replace hardcoded prompt with template call**

In `internal/compiler/concepts.go`, replace the `prompt := fmt.Sprintf(...)` block (lines 82-107) with:

```go
		prompt, renderErr := prompts.Render("extract_concepts", prompts.ExtractData{
			ExistingConcepts: strings.Join(dedup, ", "),
			Summaries:        strings.Join(summaryTexts, "\n\n---\n\n"),
		})
		if renderErr != nil {
			log.Warn("template render failed, using legacy prompt", "error", renderErr)
			prompt = fmt.Sprintf(`Extract concepts from these summaries of recently added/modified sources.

## Existing concepts (do not duplicate — merge with these when appropriate):
%s

## New/updated summaries:
%s

For each concept, provide:
- name: lowercase-hyphenated identifier (e.g., "self-attention")
- aliases: alternative names (e.g., ["scaled dot-product attention"])
- sources: which source file paths mention this concept
- type: one of "concept", "technique", or "claim"

IMPORTANT filtering rules:
- Only extract concepts that would warrant a standalone encyclopedia article
- Do NOT extract: math notation ($O(n)$, $x^2$), register names ($a0, $t1),
  single letters, numbers, file paths, or code syntax
- Do NOT extract overly generic terms (e.g., "method", "system", "data")
- Minimum: concept name should be at least 2 words or a recognized technical term

Merge with existing concepts when you detect aliases or synonyms.
Output ONLY a JSON array of objects. No markdown, no explanation.`,
				strings.Join(dedup, ", "),
				strings.Join(summaryTexts, "\n\n---\n\n"),
			)
		}
```

Add `"github.com/xoai/sage-wiki/internal/prompts"` to the imports.

- [ ] **Step 2: Build and test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 3: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/compiler/concepts.go
git commit -m "feat: switch concept extraction to use prompts.Render template system

The extract_concepts template is now used for concept extraction.
Falls back to the legacy hardcoded prompt if template rendering fails."
```

---

### Task 7: Ontology relation type config (G5)

**Files:**
- Create: `internal/ontology/relation_config.go`
- Create: `internal/ontology/relation_config_test.go`
- Modify: `internal/ontology/ontology.go:20-29`

- [ ] **Step 1: Write tests**

Create `internal/ontology/relation_config_test.go`:

```go
package ontology

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestDefaultRelationTypeDefs(t *testing.T) {
	defs := DefaultRelationTypeDefs()
	if len(defs) != 8 {
		t.Errorf("expected 8 default relation types, got %d", len(defs))
	}
	// Check first one
	if defs[0].Name != "implements" {
		t.Errorf("expected first type 'implements', got %q", defs[0].Name)
	}
}

func TestValidRelation(t *testing.T) {
	defs := []config.RelationTypeDef{
		{Name: "implements", Synonyms: []string{"实现了", "implementation of"}},
		{Name: "amends", Synonyms: []string{"修订", "废止"}},
	}

	tests := []struct {
		relation string
		valid    bool
	}{
		{"implements", true},
		{"amends", true},
		{"实现了", true},     // synonym
		{"修订", true},       // synonym
		{"unknown", false},
	}

	for _, tt := range tests {
		got := ValidRelation(tt.relation, defs)
		if got != tt.valid {
			t.Errorf("ValidRelation(%q) = %v, want %v", tt.relation, got, tt.valid)
		}
	}
}

func TestNormalizeRelation(t *testing.T) {
	defs := []config.RelationTypeDef{
		{Name: "implements", Synonyms: []string{"实现了", "implementation of"}},
		{Name: "amends", Synonyms: []string{"修订", "废止"}},
	}

	tests := []struct {
		input    string
		expected string
	}{
		{"implements", "implements"},
		{"实现了", "implements"},
		{"修订", "amends"},
		{"废止", "amends"},
		{"unknown", "unknown"}, // passthrough
	}

	for _, tt := range tests {
		got := NormalizeRelation(tt.input, defs)
		if got != tt.expected {
			t.Errorf("NormalizeRelation(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/ontology/ -run TestValidRelation -v`
Expected: FAIL — functions not defined

- [ ] **Step 3: Implement relation_config.go**

Create `internal/ontology/relation_config.go`:

```go
package ontology

import (
	"github.com/xoai/sage-wiki/internal/config"
)

// DefaultRelationTypeDefs returns the built-in relation types matching
// the original hardcoded constants.
func DefaultRelationTypeDefs() []config.RelationTypeDef {
	return []config.RelationTypeDef{
		{Name: RelImplements},
		{Name: RelExtends},
		{Name: RelOptimizes},
		{Name: RelContradicts},
		{Name: RelCites},
		{Name: RelPrerequisiteOf},
		{Name: RelTradesOff},
		{Name: RelDerivedFrom},
	}
}

// ValidRelation checks if a relation string matches any configured type
// (by name or synonym).
func ValidRelation(relation string, configured []config.RelationTypeDef) bool {
	for _, rt := range configured {
		if relation == rt.Name {
			return true
		}
		for _, syn := range rt.Synonyms {
			if relation == syn {
				return true
			}
		}
	}
	return false
}

// NormalizeRelation maps a synonym to its canonical relation name.
// Returns the input unchanged if no match is found.
func NormalizeRelation(relation string, configured []config.RelationTypeDef) string {
	for _, rt := range configured {
		if relation == rt.Name {
			return rt.Name
		}
		for _, syn := range rt.Synonyms {
			if relation == syn {
				return rt.Name
			}
		}
	}
	return relation
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go test ./internal/ontology/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/ontology/relation_config.go internal/ontology/relation_config_test.go
git commit -m "feat: add configurable relation type validation and normalization

ValidRelation checks against config-defined types with synonym support.
NormalizeRelation maps synonyms to canonical names.
DefaultRelationTypeDefs provides backward-compatible defaults."
```

---

### Task 8: Remove DB CHECK constraint + migration (G6)

**Files:**
- Modify: `internal/storage/db.go:191-202`

- [ ] **Step 1: Remove CHECK constraint from schema**

In `internal/storage/db.go`, replace lines 191-202:

```sql
-- Ontology: relations
CREATE TABLE IF NOT EXISTS relations (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation TEXT NOT NULL CHECK(relation IN (
		'implements','extends','optimizes','contradicts',
		'cites','prerequisite_of','trades_off','derived_from'
	)),
	metadata JSON,
	created_at TEXT,
	UNIQUE(source_id, target_id, relation)
);
```

with:

```sql
-- Ontology: relations (validation at application layer via ontology.ValidRelation)
CREATE TABLE IF NOT EXISTS relations (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation TEXT NOT NULL,
	metadata JSON,
	created_at TEXT,
	UNIQUE(source_id, target_id, relation)
);
```

- [ ] **Step 2: Add migration function**

Add a migration function in `internal/storage/db.go` (after the schema constant or at end of file):

```go
// MigrateRelationsDropCheck detects if the relations table has a CHECK
// constraint and rebuilds it without the constraint. This is needed because
// SQLite does not support ALTER TABLE DROP CONSTRAINT.
func (db *DB) MigrateRelationsDropCheck() error {
	// Check if table has CHECK constraint by inspecting schema
	var sql string
	err := db.ReadDB().QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='relations'",
	).Scan(&sql)
	if err != nil {
		return nil // table doesn't exist yet, will be created fresh
	}

	if !strings.Contains(sql, "CHECK") {
		return nil // already migrated
	}

	return db.WriteTx(func(tx *sql.Tx) error {
		// Rename old table
		if _, err := tx.Exec("ALTER TABLE relations RENAME TO relations_old"); err != nil {
			return fmt.Errorf("migrate: rename: %w", err)
		}

		// Create new table without CHECK
		if _, err := tx.Exec(`CREATE TABLE relations (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation TEXT NOT NULL,
			metadata JSON,
			created_at TEXT,
			UNIQUE(source_id, target_id, relation)
		)`); err != nil {
			return fmt.Errorf("migrate: create: %w", err)
		}

		// Copy data
		if _, err := tx.Exec(
			"INSERT INTO relations SELECT * FROM relations_old",
		); err != nil {
			return fmt.Errorf("migrate: copy: %w", err)
		}

		// Drop old table
		if _, err := tx.Exec("DROP TABLE relations_old"); err != nil {
			return fmt.Errorf("migrate: drop old: %w", err)
		}

		// Recreate indexes
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id)"); err != nil {
			return fmt.Errorf("migrate: index source: %w", err)
		}
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id)"); err != nil {
			return fmt.Errorf("migrate: index target: %w", err)
		}
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_relations_type ON relations(relation)"); err != nil {
			return fmt.Errorf("migrate: index type: %w", err)
		}

		return nil
	})
}
```

Add `"strings"` to the db.go imports if not present.

- [ ] **Step 3: Call migration on DB open**

Find the `Open()` or `New()` function in db.go that initializes the database. Add a call to `MigrateRelationsDropCheck()` after the initial schema creation:

```go
// After schema initialization
if err := db.MigrateRelationsDropCheck(); err != nil {
    log.Warn("relations migration failed", "error", err)
    // non-fatal — old constraint still works for default types
}
```

- [ ] **Step 4: Build and test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 5: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/storage/db.go
git commit -m "feat: remove relation CHECK constraint, add auto-migration

Relation type validation moves to application layer (ontology.ValidRelation).
Auto-migration detects old CHECK constraint and rebuilds table in a
transaction. Existing data is preserved."
```

---

### Task 9: extractRelations uses config (G8)

**Files:**
- Modify: `internal/compiler/write.go:290-337`

- [ ] **Step 1: Update extractRelations to accept config**

In `internal/compiler/write.go`, change the `extractRelations` function signature and body.

Change line 292:
```go
func extractRelations(conceptID string, content string, ontStore *ontology.Store) {
```
to:
```go
func extractRelations(conceptID string, content string, ontStore *ontology.Store, relationDefs []config.RelationTypeDef) {
```

Replace the hardcoded `relationPatterns` block (lines 308-318) with:

```go
	// Build relation patterns from config
	type relPattern struct {
		keywords []string
		relation string
	}

	var relationPatterns []relPattern
	for _, rd := range relationDefs {
		kws := append([]string{rd.Name}, rd.Synonyms...)
		relationPatterns = append(relationPatterns, relPattern{
			keywords: kws,
			relation: rd.Name,
		})
	}

	// If no config, use hardcoded defaults for backward compat
	if len(relationPatterns) == 0 {
		relationPatterns = []relPattern{
			{[]string{"implements", "implementation of", "is an implementation", "实现了", "实现方式"}, ontology.RelImplements},
			{[]string{"extends", "extension of", "builds on", "builds upon", "扩展了", "基于"}, ontology.RelExtends},
			{[]string{"optimizes", "optimization of", "improves upon", "faster than", "优化了", "改进了", "提升了"}, ontology.RelOptimizes},
			{[]string{"contradicts", "conflicts with", "disagrees with", "challenges", "矛盾", "冲突", "挑战了"}, ontology.RelContradicts},
			{[]string{"prerequisite", "requires knowledge of", "depends on", "built on top of", "前提", "依赖于", "前置条件"}, ontology.RelPrerequisiteOf},
			{[]string{"trade-off", "tradeoff", "trades off", "at the cost of", "取舍", "权衡", "代价是"}, ontology.RelTradesOff},
		}
	}
```

Add `"github.com/xoai/sage-wiki/internal/config"` to write.go imports.

- [ ] **Step 2: Update the call site**

In `internal/compiler/write.go` line 173, change:
```go
extractRelations(concept.Name, articleContent, ontStore)
```
to:
```go
extractRelations(concept.Name, articleContent, ontStore, nil)
```

This passes nil for now — we'll thread config in when the full pipeline has access. The nil fallback uses hardcoded defaults.

Note: To thread config properly, the `writeOneArticle` function (line 76) needs access to `cfg`. Check if it's available in the calling chain. If `WriteArticles` (line 29) receives `cfg *config.Config`, pass `cfg.Ontology.RelationTypes` (with nil check). If not, nil is fine — it falls back to defaults.

- [ ] **Step 3: Build and test**

Run: `cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 4: Commit**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git add internal/compiler/write.go
git commit -m "feat: extractRelations reads relation types from config

Relation patterns are now built from config-defined relation types
with their synonyms. Falls back to hardcoded defaults when no config
is provided."
```

---

### Task 10: Chinese prompt templates (T1-T4)

**Files:**
- Create: `~/claude-workspace/wiki/prompts/summarize-regulation.md`
- Create: `~/claude-workspace/wiki/prompts/summarize-research.md`
- Create: `~/claude-workspace/wiki/prompts/summarize-interview.md`
- Create: `~/claude-workspace/wiki/prompts/summarize-announcement.md`

- [ ] **Step 1: Create summarize-regulation.md**

Write to `~/claude-workspace/wiki/prompts/summarize-regulation.md`:

```
你是一名法规分析师，负责为知识库创建法规文档摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 法规概要
法规全称、发布机构、文号、发布/生效日期。

## 适用范围
适用的主体类型、交易类型、市场范围。

## 核心条款
按重要性列出主要条款要点（不超过 10 条）。

## 关键定义
法规中定义的重要术语及其含义。

## 修订沿革
该法规修订/废止了哪些旧规，被哪些新规引用或修订（如文中提及）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。引用原文条款时标注条号。
```

- [ ] **Step 2: Create summarize-research.md**

Write to `~/claude-workspace/wiki/prompts/summarize-research.md`:

```
你是一名卖方研究分析师，负责为知识库创建研究报告摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 报告概要
研报标题、发布机构、分析师、发布日期、覆盖标的。

## 核心观点
研报的主要投资逻辑和结论（评级、目标价如有）。

## 关键数据
支撑结论的核心数据点（财务指标、行业数据等），保留原始数字。

## 核心假设
结论依赖的关键假设（市场假设、增速假设等）。

## 风险因素
研报提及的主要风险。

## 产业链要点
涉及的上下游关系、竞争格局、技术路线（如适用）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。数字和百分比保留原文精度。
```

- [ ] **Step 3: Create summarize-interview.md**

Write to `~/claude-workspace/wiki/prompts/summarize-interview.md`:

```
你是一名行业研究员，负责为知识库创建专家访谈纪要摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 访谈概要
访谈主题、专家背景（如文中提及）、访谈日期。

## 关键判断
专家对行业/公司/技术的核心判断和预测。

## 数据点
专家提供的具体数据（市场规模、渗透率、价格、产能等），保留原始数字。

## 产业链洞察
供应链关系、技术路线选择、竞争格局等行业结构信息。

## 争议与不确定性
专家表示不确定或行业存在分歧的领域。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。区分事实陈述和专家判断。
```

- [ ] **Step 4: Create summarize-announcement.md**

Write to `~/claude-workspace/wiki/prompts/summarize-announcement.md`:

```
你是一名投行分析师，负责为知识库创建上市公司公告摘要。所有输出必须使用中文。

源文件: {{.SourcePath}}
源类型: {{.SourceType}}

请按以下结构撰写摘要：

## 公告概要
公告标题、发布公司（股票代码）、发布日期、公告类型。

## 交易结构
交易标的、交易对手、交易方式（现金/股份/混合）、交易对价。

## 关键条款
业绩承诺、锁定期、特殊条款等核心条款。

## 审批进度
已完成和待完成的审批流程（董事会/股东会/证监会等）。

## 财务影响
对上市公司的财务影响（EPS、资产负债率等，如文中提及）。

## 关键术语
逗号分隔列表，中英文对照。

摘要控制在 {{.MaxTokens}} token 以内。金额保留原文精度，标注币种。
```

- [ ] **Step 5: Verify template syntax**

Run a quick Go template parse check:

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go run -tags verify_templates cmd/sage-wiki/main.go --help 2>/dev/null || echo "Skip — templates validated at runtime by LoadFromDir"
```

Alternatively, do a quick build+test to verify no template parse errors:

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki && go build ./cmd/sage-wiki/ && go test ./...
```

- [ ] **Step 6: Commit**

```bash
cd /Users/kellen/claude-workspace/wiki
git add prompts/summarize-regulation.md prompts/summarize-research.md \
  prompts/summarize-interview.md prompts/summarize-announcement.md
git commit -m "feat: add domain-specific Chinese summarize templates

Four new templates for content types: regulation (法规), research (研报),
interview (专家访谈), announcement (交易公告). Each extracts
domain-relevant fields (e.g., 条号/评级/交易结构)."
```

Note: The wiki directory may not be a git repo. If so, just note the files are created.

---

### Task 11: Update config.yaml (C-class, fork only)

**Files:**
- Modify: `~/claude-workspace/wiki/config.yaml`

- [ ] **Step 1: Add type_signals and ontology sections**

Append to the end of `~/claude-workspace/wiki/config.yaml`:

```yaml

# 内容类型信号——按优先级匹配，第一个命中的生效
type_signals:
  - type: regulation
    filename_keywords: ["法规", "办法", "规定", "准则", "通知", "公告令", "指引"]
    content_keywords: ["第一条", "第二条", "为了规范", "根据《", "自发布之日起施行", "特此通知"]
    min_content_hits: 2
  - type: research
    filename_keywords: ["研报", "深度报告", "行业报告", "专题报告"]
    content_keywords: ["投资评级", "目标价", "盈利预测", "风险提示", "首次覆盖", "买入", "增持", "中性"]
    min_content_hits: 2
  - type: interview
    filename_keywords: ["纪要", "访谈", "调研", "专家"]
    content_keywords: ["专家表示", "Q:", "A:", "问:", "答:", "访谈纪要", "调研纪要"]
    min_content_hits: 2
  - type: announcement
    filename_keywords: ["公告", "报告书", "意见书"]
    content_keywords: ["上市公司", "股票代码", "证券简称", "重大资产重组", "交易对方"]
    min_content_hits: 2

# 本体配置
ontology:
  max_relation_types: 20
  max_content_types: 10
  relation_types:
    - name: implements
      synonyms: ["实现了", "implementation of"]
    - name: extends
      synonyms: ["扩展了", "基于", "extension of", "builds on"]
    - name: optimizes
      synonyms: ["优化了", "改进了", "提升了", "optimization of"]
    - name: contradicts
      synonyms: ["矛盾", "冲突", "挑战了", "conflicts with"]
    - name: cites
      synonyms: ["引用", "参见", "references"]
    - name: prerequisite_of
      synonyms: ["前提", "前置条件", "依赖于", "requires"]
    - name: trades_off
      synonyms: ["取舍", "权衡", "代价是", "at the cost of"]
    - name: derived_from
      synonyms: ["源自", "派生自", "based on"]
    - name: amends
      synonyms: ["修订", "修改", "废止", "替代", "supersedes"]
    - name: regulates
      synonyms: ["规范", "约束", "适用于", "governs"]
    - name: supplies
      synonyms: ["供应", "提供", "上游", "supplier of"]
    - name: competes_with
      synonyms: ["竞争", "替代方案", "competitor"]
```

- [ ] **Step 2: Validate config loads correctly**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go run ./cmd/sage-wiki/ --project-dir ~/claude-workspace/wiki status 2>&1 | head -5
```

Expected: No parse errors. If the CLI doesn't have a `status` command, just verify the build passes.

- [ ] **Step 3: Commit (wiki repo if applicable)**

Note: Config changes are C-class (fork only), tracked in the wiki data directory.

---

### Task 12: Final verification

- [ ] **Step 1: Full build + test**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
go build -o sage-wiki ./cmd/sage-wiki/
go test ./...
```

Expected: Build succeeds, ALL tests pass

- [ ] **Step 2: Verify template loading**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
ls ~/claude-workspace/wiki/prompts/
```

Expected: 9 files — `caption-image.md`, `extract-concepts.md`, `summarize-article.md`, `summarize-paper.md`, `write-article.md`, `summarize-regulation.md`, `summarize-research.md`, `summarize-interview.md`, `summarize-announcement.md`

- [ ] **Step 3: Review git log**

```bash
cd /Users/kellen/claude-workspace/projects/KarpathyWiki/sage-wiki
git log --oneline -10
```

Expected: 9 clean commits from Tasks 1-9 (Task 10-11 are in the wiki data dir)

- [ ] **Step 4: Small batch compile test (human review)**

Run compilation on a small sample of each content type:
```bash
# This will be done via mcp__sage-wiki__wiki_compile with specific files
# Select 5 files per type for human review
```

This step requires manual execution and human review of compilation quality.
