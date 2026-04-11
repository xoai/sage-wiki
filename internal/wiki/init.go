package wiki

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/storage"
)

// InitGreenfield creates a new sage-wiki project from scratch.
func InitGreenfield(dir string, project string, model string) error {
	// Create directories
	dirs := []string{
		filepath.Join(dir, "raw"),
		filepath.Join(dir, "wiki", "summaries"),
		filepath.Join(dir, "wiki", "concepts"),
		filepath.Join(dir, "wiki", "connections"),
		filepath.Join(dir, "wiki", "outputs"),
		filepath.Join(dir, "wiki", "images"),
		filepath.Join(dir, "wiki", "archive"),
		filepath.Join(dir, ".sage"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("init: create %s: %w", d, err)
		}
	}

	// Write config template with comments
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgContent := configTemplate(project, fmt.Sprintf("sage-wiki project: %s", project), false, model)
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		return fmt.Errorf("init: save config: %w", err)
	}

	// Create SQLite DB
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("init: create db: %w", err)
	}
	db.Close()

	// Write .gitignore
	gitignore := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignore, []byte(".sage/\n"), 0644); err != nil {
		return fmt.Errorf("init: write .gitignore: %w", err)
	}

	// Write empty manifest
	manifestPath := filepath.Join(dir, ".manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"version":2,"sources":{},"concepts":{}}`+"\n"), 0644); err != nil {
		return fmt.Errorf("init: write manifest: %w", err)
	}

	// Git init
	if gitpkg.IsAvailable() {
		if err := gitpkg.Init(dir); err != nil {
			log.Warn("git init failed", "error", err)
		}
	}

	log.Info("project initialized", "mode", "greenfield", "dir", dir)
	return nil
}

// InitVaultOverlay initializes sage-wiki on an existing Obsidian vault.
func InitVaultOverlay(dir string, project string, sourceFolders []string, ignoreFolders []string, output string, model string) error {
	if output == "" {
		output = "_wiki"
	}

	// Create output directories
	outputDir := filepath.Join(dir, output)
	subdirs := []string{"summaries", "concepts", "connections", "outputs", "images", "archive"}
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(outputDir, sub), 0755); err != nil {
			return fmt.Errorf("init: create %s: %w", sub, err)
		}
	}

	// Create .sage
	if err := os.MkdirAll(filepath.Join(dir, ".sage"), 0755); err != nil {
		return fmt.Errorf("init: create .sage: %w", err)
	}

	// Build config template
	// Build sources YAML
	var sourcesYAML string
	for _, sf := range sourceFolders {
		sourcesYAML += fmt.Sprintf("  - path: %s\n    type: article\n    watch: true\n", sf)
	}

	ignoreList := append(ignoreFolders, output)
	var ignoreYAML string
	for _, ig := range ignoreList {
		ignoreYAML += fmt.Sprintf("  - %s\n", ig)
	}

	cfgContent := configTemplateVault(project, output, sourcesYAML, ignoreYAML, model)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
		return fmt.Errorf("init: save config: %w", err)
	}

	// Create SQLite DB
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("init: create db: %w", err)
	}
	db.Close()

	// Write manifest
	manifestPath := filepath.Join(dir, ".manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"version":2,"sources":{},"concepts":{}}`+"\n"), 0644); err != nil {
		return fmt.Errorf("init: write manifest: %w", err)
	}

	log.Info("project initialized", "mode", "vault-overlay", "dir", dir, "sources", sourceFolders)
	return nil
}

// ScanVaultFolders scans a directory and returns folder names with file counts.
type FolderInfo struct {
	Name      string
	FileCount int
	HasMD     bool
	HasPDF    bool
}

// ScanFolders lists top-level folders with file statistics.
func ScanFolders(dir string) ([]FolderInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	var folders []FolderInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden and system folders
		if strings.HasPrefix(name, ".") || name == "_wiki" {
			continue
		}

		info := FolderInfo{Name: name}
		filepath.WalkDir(filepath.Join(dir, name), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info.FileCount++
			ext := strings.ToLower(filepath.Ext(path))
			if ext == ".md" {
				info.HasMD = true
			} else if ext == ".pdf" {
				info.HasPDF = true
			}
			return nil
		})

		folders = append(folders, info)
	}

	return folders, nil
}

func configTemplate(project, description string, isVault bool, model string) string {
	return fmt.Sprintf(`# sage-wiki configuration
# Docs: https://github.com/xoai/sage-wiki

version: 1
project: %s
description: "%s"

sources:
  - path: raw
    type: auto          # auto-detect from file extension
    watch: true

output: wiki

# LLM provider configuration
# Supported: anthropic, openai, gemini, ollama, openai-compatible
# For OpenRouter or other OpenAI-compatible providers, set:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}
  # base_url:           # custom endpoint (OpenRouter, Azure, local proxy, etc.)
  # rate_limit: 60      # requests per minute (default: auto per provider)

# Model selection per task
# Use faster/cheaper models for high-volume tasks, quality models for writing
models:
  summarize: %s
  extract: %s
  write: %s
  lint: %s
  query: %s

# Embedding configuration (optional — auto-detected from api provider)
# Override to use a different provider/model for embeddings
embed:
  provider: auto        # auto, openai, gemini, ollama, voyage, mistral
  # model:              # override model (e.g., text-embedding-3-small)
  # api_key:            # separate API key for embeddings
  # base_url:           # separate endpoint for embeddings

compiler:
  max_parallel: 4
  debounce_seconds: 2
  summary_max_tokens: 2000
  article_max_tokens: 4000
  auto_commit: true
  auto_lint: true
  # timezone: Asia/Shanghai   # IANA timezone for user-facing timestamps (default: UTC)
  # article_fields:           # custom frontmatter fields extracted from LLM response
  #   - language
  #   - domain

search:
  hybrid_weight_bm25: 0.7
  hybrid_weight_vector: 0.3
  default_limit: 10

serve:
  transport: stdio      # stdio or sse
  port: 3333            # SSE mode only

# Ontology types (optional)
# Extend built-in types with additional synonyms or add custom types.
#
# Built-in relation types: implements, extends, optimizes, contradicts, cites,
#                          prerequisite_of, trades_off, derived_from
# Built-in entity types: concept, technique, source, claim, artifact
#
# ontology:
#   relations:
#     - name: implements
#       synonyms: ["thực hiện", "triển khai"]   # add Vietnamese synonyms
#     - name: regulates
#       synonyms: ["regulates", "regulated by", "调控", "调节"]
#   entity_types:
#     - name: conversation
#       description: "A dialogue or discussion"
#     - name: decision
#       description: "A recorded decision with rationale"
`, project, description, model, model, model, model, model)
}

func configTemplateVault(project, output, sourcesYAML, ignoreYAML, model string) string {
	return fmt.Sprintf(`# sage-wiki configuration (vault overlay)
# Docs: https://github.com/xoai/sage-wiki

version: 1
project: %s
description: "Obsidian vault with sage-wiki: %s"

vault:
  root: .

sources:
%s
output: %s

ignore:
%s
# LLM provider configuration
# Supported: anthropic, openai, gemini, ollama, openai-compatible
# For OpenRouter or other OpenAI-compatible providers, set:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}
  # base_url:           # custom endpoint (OpenRouter, Azure, local proxy, etc.)
  # rate_limit: 60      # requests per minute

models:
  summarize: %s
  extract: %s
  write: %s
  lint: %s
  query: %s

# Embedding configuration (optional — auto-detected from api provider)
embed:
  provider: auto
  # model:              # override embedding model
  # api_key:            # separate API key for embeddings
  # base_url:           # separate endpoint for embeddings

compiler:
  max_parallel: 4
  auto_commit: true
  auto_lint: true

search:
  hybrid_weight_bm25: 0.7
  hybrid_weight_vector: 0.3

serve:
  transport: stdio

# Ontology types (optional)
# ontology:
#   relations:
#     - name: implements
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates
#       synonyms: ["regulates", "regulated by"]
#   entity_types:
#     - name: conversation
#     - name: decision
`, project, project, sourcesYAML, output, ignoreYAML, model, model, model, model, model)
}
