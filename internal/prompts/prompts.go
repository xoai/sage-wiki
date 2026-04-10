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
var activeTemplates *template.Template

func init() {
	var err error
	defaultTemplates, err = template.ParseFS(templateFS, "templates/*.txt")
	if err != nil {
		panic(fmt.Sprintf("prompts: failed to parse embedded templates: %v", err))
	}
	activeTemplates = defaultTemplates
}

// LoadFromDir loads user prompt templates from a directory.
// Templates in the directory override embedded defaults by filename.
// Files should be named like: summarize-article.md, write-article.md, etc.
// Falls back to embedded defaults for any template not found in the directory.
func LoadFromDir(dir string) error {
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
	}

	if loaded > 0 {
		activeTemplates = merged
	}

	return nil
}

// Render renders a named template with the given data.
// Uses user overrides if loaded, otherwise embedded defaults.
func Render(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := activeTemplates.ExecuteTemplate(&buf, name+".txt", data); err != nil {
		return "", fmt.Errorf("prompts.Render(%s): %w", name, err)
	}
	return buf.String(), nil
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

// Available returns the names of all loaded templates.
func Available() []string {
	var names []string
	for _, t := range activeTemplates.Templates() {
		names = append(names, t.Name())
	}
	return names
}

// Reset restores embedded defaults (useful for testing).
func Reset() {
	activeTemplates = defaultTemplates
}
