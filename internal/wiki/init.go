package wiki

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/storage"
)

// InitOption configures InitGreenfield/InitVaultOverlay behavior.
type InitOption func(*initOptions)

type initOptions struct{ force bool }

// WithForce makes init rewrite .gitignore and .manifest.json even when they
// exist. config.yaml is preserved unconditionally (#84) — force does NOT
// overwrite it. Default (no option) preserves all three.
func WithForce(force bool) InitOption {
	return func(o *initOptions) { o.force = force }
}

// InitGreenfield creates a new sage-wiki project from scratch.
func InitGreenfield(dir string, project string, model string, opts ...InitOption) error {
	var io initOptions
	for _, opt := range opts {
		opt(&io)
	}
	// Create directories. Note: "connections" is intentionally NOT created —
	// concept-to-concept relations live in the SQLite relations table and
	// surface via `sage-wiki ontology query`, the web UI graph, and the
	// linter's ConnectionsPass. An empty connections/ dir confused users into
	// thinking the feature was broken (#91).
	dirs := []string{
		filepath.Join(dir, "raw"),
		filepath.Join(dir, "wiki", "summaries"),
		filepath.Join(dir, "wiki", "concepts"),
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

	// Write config template — but preserve existing config so users can
	// safely re-run `sage-wiki init` after deleting .sage/ to recover state.
	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfgContent := configTemplate(project, fmt.Sprintf("sage-wiki project: %s", project), false, model)
		if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
			return fmt.Errorf("init: save config: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "config.yaml already exists, preserving it\n")
	}

	// Create SQLite DB
	// P2-1 skip-list: init bootstraps the sqlite index file even when a
	// preserved config selects postgres — postgres bootstrap is reconcile/
	// recompile from files (design D10), not init (decisions.md 2026-07-21).
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("init: create db: %w", err)
	}
	db.Close()

	// Write .gitignore — append .sage/ when the file exists, never clobber
	// user content (#127). Force rewrites it wholesale.
	gitignore := filepath.Join(dir, ".gitignore")
	if err := writeGitignore(gitignore, io.force); err != nil {
		return fmt.Errorf("init: write .gitignore: %w", err)
	}

	// Write empty manifest — skip when one exists: wiping it orphans every
	// compiled output and (post-P3-7) silences the startup reconcile guard.
	// Force rewrites it.
	manifestPath := filepath.Join(dir, ".manifest.json")
	if err := writeEmptyManifest(manifestPath, io.force); err != nil {
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
func InitVaultOverlay(dir string, project string, sourceFolders []string, ignoreFolders []string, output string, model string, opts ...InitOption) error {
	var io initOptions
	for _, opt := range opts {
		opt(&io)
	}
	if output == "" {
		output = "_wiki"
	}

	// Create output directories. See InitGreenfield re: "connections" — same
	// rationale (#91).
	outputDir := filepath.Join(dir, output)
	subdirs := []string{"summaries", "concepts", "outputs", "images", "archive"}
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

	cfgPath := filepath.Join(dir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfgContent := configTemplateVault(project, output, sourcesYAML, ignoreYAML, model)
		if err := os.WriteFile(cfgPath, []byte(cfgContent), 0644); err != nil {
			return fmt.Errorf("init: save config: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "config.yaml already exists, preserving it\n")
	}

	// Create SQLite DB
	// P2-1 skip-list: init bootstraps the sqlite index file even when a
	// preserved config selects postgres — postgres bootstrap is reconcile/
	// recompile from files (design D10), not init (decisions.md 2026-07-21).
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return fmt.Errorf("init: create db: %w", err)
	}
	db.Close()

	// Write .gitignore — vaults are usually git-tracked Obsidian vaults;
	// .sage/ belongs ignored there too (review: was greenfield-only).
	gitignore := filepath.Join(dir, ".gitignore")
	if err := writeGitignore(gitignore, io.force); err != nil {
		return fmt.Errorf("init: write .gitignore: %w", err)
	}

	// Write manifest — same preservation rule as greenfield (#127: the vault
	// path had the identical unconditional-overwrite bug).
	manifestPath := filepath.Join(dir, ".manifest.json")
	if err := writeEmptyManifest(manifestPath, io.force); err != nil {
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
# Supported: anthropic, openai, gemini, ollama, openai-compatible, qwen
# For OpenRouter or other OpenAI-compatible providers, set:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# For Alibaba Cloud DashScope Qwen, set:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
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
  summary_max_tokens: 4000
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
#   relation_types:
#     - name: implements
#       synonyms: ["thực hiện", "triển khai"]   # add Vietnamese synonyms
#     - name: regulates
#       synonyms: ["regulates", "regulated by", "调控", "调节"]
#   entity_types:
#     - name: conversation
#       description: "A dialogue or discussion"
#     - name: decision
#       description: "A recorded decision with rationale"
#   triples:                      # LLM triple extraction (opt-in, costs 1 call/doc)
#     enabled: true
#     model: ""                   # default: models.extract, then models.summarize
#     max_entities_per_doc: 40
#     max_relations_per_doc: 60
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
# Supported: anthropic, openai, gemini, ollama, openai-compatible, qwen
# For OpenRouter or other OpenAI-compatible providers, set:
#   provider: openai-compatible
#   base_url: https://openrouter.ai/api/v1
# For Alibaba Cloud DashScope Qwen, set:
#   provider: qwen
#   api_key: ${DASHSCOPE_API_KEY}
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
#   relation_types:
#     - name: implements
#       synonyms: ["thực hiện", "triển khai"]
#     - name: regulates
#       synonyms: ["regulates", "regulated by"]
#   entity_types:
#     - name: conversation
#     - name: decision
#   triples:                      # LLM triple extraction (opt-in, costs 1 call/doc)
#     enabled: true
#     max_entities_per_doc: 40
#     max_relations_per_doc: 60
`, project, project, sourcesYAML, output, ignoreYAML, model, model, model, model, model)
}

// writeGitignore creates .gitignore with .sage/ when absent, appends a
// line-exact .sage/ entry to an existing file (a commented "# .sage/" line
// does NOT count), and rewrites wholesale under force.
func writeGitignore(path string, force bool) error {
	if force {
		return os.WriteFile(path, []byte(".sage/\n"), 0644)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(path, []byte(".sage/\n"), 0644)
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		// git strips TRAILING whitespace from ignore patterns but NOT
		// leading — a " .sage/" line does not ignore the dir, so it must not
		// count as present (review). ".sage" (no slash) also covers the dir.
		trimmed := strings.TrimRight(line, " \t\r")
		if trimmed == ".sage/" || trimmed == ".sage" {
			return nil // already ignored
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + ".sage/\n")
	return err
}

// writeEmptyManifest writes the empty starting manifest unless the file
// already exists (preserved, like config.yaml) or force is set.
// writeEmptyManifest writes the empty starting manifest unless the file
// already exists AND parses as JSON (preserved, like config.yaml) or force
// is set. A 0-byte or corrupt file left by an interrupted write is treated
// as absent — re-init becomes self-healing instead of preserving a file
// manifest.Load will choke on at every startup (review).
func writeEmptyManifest(path string, force bool) error {
	if !force {
		if data, err := os.ReadFile(path); err == nil {
			var probe map[string]any
			if json.Unmarshal(data, &probe) == nil {
				fmt.Fprintf(os.Stderr, ".manifest.json already exists, preserving it\n")
				return nil
			}
			fmt.Fprintf(os.Stderr, ".manifest.json is corrupt — reinitializing (back it up if needed)\n")
		}
	}
	data, err := json.Marshal(manifest.NewWithClock(config.NowUTC))
	if err != nil {
		return fmt.Errorf("init: marshal manifest: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}
