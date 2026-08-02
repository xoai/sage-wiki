package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.txt
var templateFS embed.FS

var defaultTemplates *template.Template

func init() {
	var err error
	defaultTemplates, err = template.ParseFS(templateFS, "templates/*.txt")
	if err != nil {
		panic(fmt.Sprintf("prompts: failed to parse embedded templates: %v", err))
	}
	defaultRegistry = NewRegistry()
}

// Registry is an independent template set: embedded defaults plus any user
// overrides loaded from a directory. Per-workspace prompt overrides are a
// Registry per workspace (SPEC-01) — two workspaces never share render
// state. Build with NewRegistry.
type Registry struct {
	templates *template.Template
	// overrides records which template names came from which on-disk file
	// (SPEC-04): the effective-bytes hash reads the override file's raw
	// bytes, not the parse tree.
	overrides map[string]string
}

// NewRegistry returns a Registry holding the embedded default templates.
func NewRegistry() *Registry {
	return &Registry{templates: defaultTemplates}
}

// defaultRegistry backs the package-level API (CLI back-compat). The
// package functions delegate to it; engine code paths use their own
// per-Workspace Registry instead. Assigned in init — a package-level var
// initializer would run BEFORE defaultTemplates is parsed.
var defaultRegistry *Registry

// LoadFromDir merges user prompt templates from a directory into the
// registry. Templates in the directory override embedded defaults by
// filename. Falls back to embedded defaults for any template not found.
func (r *Registry) LoadFromDir(dir string) error {
	if dir == "" {
		return nil
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil // directory doesn't exist — use defaults silently
	}

	// Start with a clone of defaults
	merged, err := template.ParseFS(templateFS, "templates/*.txt")
	if err != nil {
		return err
	}

	// Scan user directory for .md and .txt files
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("prompts: read dir %s: %w", dir, err)
	}

	loaded := 0
	loadedOverrides := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		if ext != ".md" && ext != ".txt" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		// Map filename to template name:
		// "summarize-article.md" → "summarize_article.txt"
		// "write-article.md" → "write_article.txt"
		templateName := filenameToTemplateName(entry.Name())

		// Parse as template, overriding the default
		_, err = merged.New(templateName).Parse(string(data))
		if err != nil {
			return fmt.Errorf("prompts: parse %s: %w", entry.Name(), err)
		}
		loaded++
		loadedOverrides[templateName] = filepath.Join(dir, entry.Name())
	}

	if loaded > 0 {
		// Templates and provenance swap atomically (NEW-2: a mid-load parse
		// error must not leave overrides pointing at templates that render
		// the old body); each call rebuilds both from scratch.
		r.templates = merged
		r.overrides = loadedOverrides
	}

	return nil
}

// OverridePath returns the on-disk file backing an override template, or ""
// when the template is the embedded default (SPEC-04).
func (r *Registry) OverridePath(templateName string) string {
	if r == nil {
		return ""
	}
	return r.overrides[templateName]
}

// Render renders a named template with the given data from this registry.
// Language gating matches the package-level Render.
func (r *Registry) Render(name string, data any, language string) (string, error) {
	return renderFrom(r.templates, name, data, language)
}

// Available returns the names of all templates in the registry.
func (r *Registry) Available() []string {
	var names []string
	for _, t := range r.templates.Templates() {
		names = append(names, t.Name())
	}
	return names
}

// LoadFromDir loads user prompt templates into the DEFAULT registry
// (back-compat for CLI paths). Engine code uses a per-Workspace Registry.
func LoadFromDir(dir string) error {
	return defaultRegistry.LoadFromDir(dir)
}

// isJSONTemplate returns true if the rendered template content requires
// structured JSON output. Detected by convention: the template must contain
// "Output ONLY a JSON" or "Return ONLY a JSON" (case-insensitive).
// Language instructions are skipped for these to avoid corrupting the format.
func isJSONTemplate(rendered string) bool {
	lower := strings.ToLower(rendered)
	return strings.Contains(lower, "output only a json") ||
		strings.Contains(lower, "return only a json")
}

// Render renders a named template from the DEFAULT registry (back-compat).
// If language is non-empty, appends a language instruction
// (except for JSON-output templates, detected by convention).
func Render(name string, data any, language string) (string, error) {
	return renderFrom(defaultRegistry.templates, name, data, language)
}

func renderFrom(templates *template.Template, name string, data any, language string) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name+".txt", data); err != nil {
		return "", fmt.Errorf("prompts.Render(%s): %w", name, err)
	}
	result := buf.String()
	if language != "" && !isJSONTemplate(result) {
		result += LanguageInstruction(language)
	}
	return result, nil
}

// LanguageInstruction returns the directive appended to a prompt to produce
// output in the given language, or "" when language is empty. It is the single
// source of truth for localization wording, shared by Render and the summary
// synthesis path (summarize.go) so the two cannot drift.
//
// The wording explicitly covers the title and section headings (issue #110 —
// the body was localized but headings/title stayed English), and protects
// [[wikilink]] targets: a target is a concept-file identifier, and translating
// it would make the post-compile strip pass delete the cross-reference. This is
// a PURE string builder — it does no JSON gating; callers (Render) gate on
// isJSONTemplate themselves.
func LanguageInstruction(language string) string {
	if language == "" {
		return ""
	}
	return fmt.Sprintf("\n\nIMPORTANT: Write your entire response in %s — including any title and all section headings. Keep the following in their original form: code, identifiers, file paths, URLs, established proper nouns (product, library, and API names), and the exact text inside every [[...]] wikilink (a wikilink target is a file identifier — never translate it).", language)
}

// ScaffoldDefaults copies all embedded default templates to a directory
// for user customization. Called by `sage-wiki init --prompts`.
func ScaffoldDefaults(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		data, err := templateFS.ReadFile("templates/" + entry.Name())
		if err != nil {
			continue
		}

		// Convert to .md for user-friendliness
		outName := templateNameToFilename(entry.Name())
		outPath := filepath.Join(dir, outName)

		// Don't overwrite existing customizations
		if _, err := os.Stat(outPath); err == nil {
			continue
		}

		vars := "{{.SourcePath}}, {{.SourceType}}, {{.MaxTokens}}"
		if strings.Contains(outName, "write-article") {
			vars = "{{.ConceptName}}, {{.ConceptID}}, {{.Sources}}, {{.Aliases}}, {{.RelatedList}}, {{.ExistingArticle}}, {{.Learnings}}, {{.MaxTokens}}, {{.Confidence}}"
		} else if strings.Contains(outName, "extract-concepts") {
			vars = "{{.ExistingConcepts}}, {{.Summaries}}"
		} else if strings.Contains(outName, "extract-triples") {
			vars = "{{.ValidTypes}}, {{.ValidPredicates}}, {{.Summary}}"
		}
		header := fmt.Sprintf("# %s\n# This file customizes the sage-wiki %s prompt.\n# Edit freely — sage-wiki will use this instead of the built-in default.\n# Delete this file to revert to the default.\n#\n# Available variables: %s\n# See: https://github.com/xoai/sage-wiki\n\n", outName, strings.TrimSuffix(outName, ".md"), vars)

		if err := os.WriteFile(outPath, []byte(header+string(data)), 0644); err != nil {
			return fmt.Errorf("prompts: scaffold %s: %w", outName, err)
		}
	}

	return nil
}

// filenameToTemplateName converts user filenames to internal template names.
// "summarize-article.md" → "summarize_article.txt"
// "summarize-paper.txt" → "summarize_paper.txt"
func filenameToTemplateName(filename string) string {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	name = strings.ReplaceAll(name, "-", "_")
	return name + ".txt"
}

// templateNameToFilename converts internal template names to user-friendly filenames.
// "summarize_article.txt" → "summarize-article.md"
func templateNameToFilename(templateName string) string {
	name := strings.TrimSuffix(templateName, ".txt")
	name = strings.ReplaceAll(name, "_", "-")
	return name + ".md"
}

// SummarizeData holds data for summarize templates.
type SummarizeData struct {
	SourcePath string
	SourceType string
	MaxTokens  int
}

// ExtractData holds data for concept extraction template.
type ExtractData struct {
	ExistingConcepts string
	Summaries        string
}

// TriplesData holds data for the triple-extraction template (P3-2).
//
// Summary is the compiled summary body — NOT the raw source — so evidence spans
// quote the summary. The pass strips any frontmatter before rendering.
//
// There is deliberately no SourcePath field: the source path is folded INTO
// Summary by the caller so it sits inside the untrusted_source frame, exactly
// as concepts.go does. Rendering a filename in the prompt's trusted region
// would let a file named to carry instructions steer the pass, and
// NeutralizeTags only defangs the delimiters — it does not neutralize prose.
type TriplesData struct {
	ValidTypes      string
	ValidPredicates string
	Summary         string
}

// ResolveData holds data for the entity-resolution template (P3-3).
//
// Members is the rendered candidate block — one line per candidate, each under
// an opaque label (E1, E2, ...) with its name, type and description.
//
// Labels rather than names or ids, deliberately: Entity.ID != Entity.Name for
// every entity the compiler writes, and two rows can legally share a Name, so
// neither is a safe key in the model's response. Labels make the mapping back
// to ids total and unambiguous, and keep id spelling out of the prompt.
//
// Like TriplesData there is no separate path field: everything untrusted is
// folded into Members by the caller so it sits inside the untrusted_source
// frame.
type ResolveData struct {
	Members string
}

// WriteArticleData holds data for article writing template.
type WriteArticleData struct {
	ConceptName     string
	ConceptID       string
	Sources         string
	RelatedConcepts []string
	ExistingArticle string
	Learnings       string
	Aliases         string
	SourceList      string
	RelatedList     string
	Confidence      string
	MaxTokens       int
	SourceContext   string // relevant source sections (from document splitting)
}

// CaptionData holds data for image captioning template.
type CaptionData struct {
	SourcePath string
}

// CaptureData holds data for the knowledge capture template.
// Content is passed separately in the user message, not in the template.
type CaptureData struct {
	Context string
	Tags    string
}

// Available returns the names of all templates in the DEFAULT registry.
func Available() []string {
	return defaultRegistry.Available()
}

// Reset restores the DEFAULT registry to embedded defaults (useful for testing).
func Reset() {
	defaultRegistry = NewRegistry()
}

// ResetDefaultRegistryForTest restores the package-default registry to the
// embedded templates (drops any prompts/ overrides loaded via the
// package-level LoadFromDir). Test-only seam — production code never calls
// it (the override state is deliberately process-global, matching compile).
func ResetDefaultRegistryForTest() {
	defaultRegistry = NewRegistry()
}
