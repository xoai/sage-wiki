package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var typeNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Config represents the sage-wiki project configuration.
type Config struct {
	Version     int          `yaml:"version"`
	Project     string       `yaml:"project"`
	Description string       `yaml:"description"`
	Language    string       `yaml:"language,omitempty"`
	Vault       *VaultConfig `yaml:"vault,omitempty"`
	Sources     []Source     `yaml:"sources"`
	Output      string       `yaml:"output"`
	Ignore      []string     `yaml:"ignore,omitempty"`
	API         APIConfig    `yaml:"api"`
	Models      ModelsConfig `yaml:"models"`
	Embed       *EmbedConfig `yaml:"embed,omitempty"`
	Compiler    CompilerConfig `yaml:"compiler"`
	Search      SearchConfig   `yaml:"search"`
	Linting     LintingConfig  `yaml:"linting"`
	Serve       ServeConfig    `yaml:"serve"`
	Ontology    OntologyConfig `yaml:"ontology,omitempty"`
	TypeSignals []TypeSignal   `yaml:"type_signals,omitempty"`
}

type VaultConfig struct {
	Root string `yaml:"root"`
}

type Source struct {
	Path  string `yaml:"path"`
	Type  string `yaml:"type"`
	Watch bool   `yaml:"watch"`
}

type APIConfig struct {
	Provider  string `yaml:"provider"`
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url,omitempty"`
	RateLimit int    `yaml:"rate_limit,omitempty"`
}

type ModelsConfig struct {
	Summarize string `yaml:"summarize"`
	Extract   string `yaml:"extract"`
	Write     string `yaml:"write"`
	Lint      string `yaml:"lint"`
	Query     string `yaml:"query"`
}

type EmbedConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions,omitempty"`
	APIKey     string `yaml:"api_key,omitempty"`
	BaseURL    string `yaml:"base_url,omitempty"`
}

type CompilerConfig struct {
	MaxParallel      int     `yaml:"max_parallel"`
	DebounceSeconds  int     `yaml:"debounce_seconds"`
	SummaryMaxTokens int     `yaml:"summary_max_tokens"`
	ArticleMaxTokens int     `yaml:"article_max_tokens"`
	AutoCommit       bool    `yaml:"auto_commit"`
	AutoLint         bool    `yaml:"auto_lint"`
	Mode             string  `yaml:"mode,omitempty"`              // standard, batch, or auto
	EstimateBefore   bool    `yaml:"estimate_before,omitempty"`   // prompt with cost estimate before compiling
	PromptCache      *bool   `yaml:"prompt_cache,omitempty"`      // enable prompt caching (default: true)
	BatchThreshold   int     `yaml:"batch_threshold,omitempty"`   // min sources to auto-select batch mode
	TokenPriceOverride float64 `yaml:"token_price_per_million,omitempty"` // override price per 1M input tokens
	Timezone         string   `yaml:"timezone,omitempty"`          // IANA timezone for user-facing timestamps (default: UTC)
	ArticleFields    []string `yaml:"article_fields,omitempty"`    // custom frontmatter fields extracted from LLM response

	resolvedTZ *time.Location `yaml:"-"` // cached by Validate(); not serialized
}

type SearchConfig struct {
	HybridWeightBM25   float64 `yaml:"hybrid_weight_bm25"`
	HybridWeightVector float64 `yaml:"hybrid_weight_vector"`
	DefaultLimit       int     `yaml:"default_limit"`
	QueryExpansion     *bool   `yaml:"query_expansion,omitempty"` // enable LLM query expansion (default: true)
	Rerank             *bool   `yaml:"rerank,omitempty"`          // enable LLM re-ranking (default: true)
	ChunkSize          int     `yaml:"chunk_size,omitempty"`      // tokens per chunk for indexing (default: 800)

	// Graph-enhanced retrieval
	GraphExpansion       *bool   `yaml:"graph_expansion,omitempty"`        // enable graph-based context expansion (default: true)
	GraphMaxExpand       int     `yaml:"graph_max_expand,omitempty"`       // max articles added via graph (default: 10)
	GraphDepth           int     `yaml:"graph_depth,omitempty"`            // traversal depth for expansion (default: 2)
	ContextMaxTokens     int     `yaml:"context_max_tokens,omitempty"`     // token budget for query context (default: 8000)
	WeightDirectLink     *float64 `yaml:"weight_direct_link,omitempty"`     // graph signal weight (default: 3.0, set 0 to disable)
	WeightSourceOverlap  *float64 `yaml:"weight_source_overlap,omitempty"`  // graph signal weight (default: 4.0, set 0 to disable)
	WeightCommonNeighbor *float64 `yaml:"weight_common_neighbor,omitempty"` // graph signal weight (default: 1.5, set 0 to disable)
	WeightTypeAffinity   *float64 `yaml:"weight_type_affinity,omitempty"`   // graph signal weight (default: 1.0, set 0 to disable)
}

// QueryExpansionEnabled returns whether query expansion is enabled (default: true).
func (s SearchConfig) QueryExpansionEnabled() bool {
	if s.QueryExpansion == nil {
		return true
	}
	return *s.QueryExpansion
}

// RerankEnabled returns whether re-ranking is enabled (default: true).
func (s SearchConfig) RerankEnabled() bool {
	if s.Rerank == nil {
		return true
	}
	return *s.Rerank
}

// ChunkSizeOrDefault returns the chunk size or 800 if not set.
func (s SearchConfig) ChunkSizeOrDefault() int {
	if s.ChunkSize <= 0 {
		return 800
	}
	return s.ChunkSize
}

// GraphExpansionEnabled returns whether graph expansion is enabled (default: true).
func (s SearchConfig) GraphExpansionEnabled() bool {
	if s.GraphExpansion == nil {
		return true
	}
	return *s.GraphExpansion
}

// GraphMaxExpandOrDefault returns the max expand or 10 if not set.
func (s SearchConfig) GraphMaxExpandOrDefault() int {
	if s.GraphMaxExpand <= 0 {
		return 10
	}
	return s.GraphMaxExpand
}

// GraphDepthOrDefault returns the graph depth or 2 if not set.
func (s SearchConfig) GraphDepthOrDefault() int {
	if s.GraphDepth <= 0 {
		return 2
	}
	return s.GraphDepth
}

// ContextMaxTokensOrDefault returns the context token budget or 8000 if not set.
func (s SearchConfig) ContextMaxTokensOrDefault() int {
	if s.ContextMaxTokens <= 0 {
		return 8000
	}
	return s.ContextMaxTokens
}

// WeightDirectLinkOrDefault returns the direct link weight or 3.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightDirectLinkOrDefault() float64 {
	if s.WeightDirectLink == nil {
		return 3.0
	}
	return *s.WeightDirectLink
}

// WeightSourceOverlapOrDefault returns the source overlap weight or 4.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightSourceOverlapOrDefault() float64 {
	if s.WeightSourceOverlap == nil {
		return 4.0
	}
	return *s.WeightSourceOverlap
}

// WeightCommonNeighborOrDefault returns the common neighbor weight or 1.5 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightCommonNeighborOrDefault() float64 {
	if s.WeightCommonNeighbor == nil {
		return 1.5
	}
	return *s.WeightCommonNeighbor
}

// WeightTypeAffinityOrDefault returns the type affinity weight or 1.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightTypeAffinityOrDefault() float64 {
	if s.WeightTypeAffinity == nil {
		return 1.0
	}
	return *s.WeightTypeAffinity
}

type LintingConfig struct {
	AutoFixPasses          []string `yaml:"auto_fix_passes"`
	StalenessThresholdDays int      `yaml:"staleness_threshold_days"`
}

type ServeConfig struct {
	Transport string `yaml:"transport"`
	Port      int    `yaml:"port"`
}

// TypeSignal defines a content-based type detection rule.
// Files are matched by filename keywords and/or content keywords.
type TypeSignal struct {
	Type             string   `yaml:"type"`
	Pattern          string   `yaml:"pattern,omitempty"`           // simple substring match (legacy)
	FilenameKeywords []string `yaml:"filename_keywords,omitempty"` // keywords matched against filename
	ContentKeywords  []string `yaml:"content_keywords,omitempty"`  // keywords matched against content head
	MinContentHits   int      `yaml:"min_content_hits,omitempty"`  // minimum content keyword matches required
}

// OntologyConfig configures ontology relation and entity types.
type OntologyConfig struct {
	Relations     []RelationConfig   `yaml:"relations,omitempty"`
	RelationTypes []RelationConfig   `yaml:"relation_types,omitempty"` // preferred key; "relations" accepted for backwards compat
	EntityTypes   []EntityTypeConfig `yaml:"entity_types,omitempty"`
}

// RelationConfig defines a custom or extended relation type.
type RelationConfig struct {
	Name     string   `yaml:"name"`
	Synonyms []string `yaml:"synonyms"`
}

// EntityTypeConfig defines a custom or extended entity type.
type EntityTypeConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
}

// Defaults returns a Config with sensible defaults for greenfield mode.
func Defaults() Config {
	return Config{
		Version: 1,
		Output:  "wiki",
		Sources: []Source{{Path: "raw", Type: "auto", Watch: true}},
		Compiler: CompilerConfig{
			MaxParallel:      4,
			DebounceSeconds:  2,
			SummaryMaxTokens: 2000,
			ArticleMaxTokens: 4000,
			AutoCommit:       true,
			AutoLint:         true,
		},
		Search: SearchConfig{
			HybridWeightBM25:   0.7,
			HybridWeightVector: 0.3,
			DefaultLimit:       10,
		},
		Linting: LintingConfig{
			AutoFixPasses:          []string{"consistency", "completeness", "style"},
			StalenessThresholdDays: 90,
		},
		Serve: ServeConfig{
			Transport: "stdio",
			Port:      3333,
		},
	}
}

// PromptCacheEnabled returns whether prompt caching is enabled (default: true).
func (c *CompilerConfig) PromptCacheEnabled() bool {
	if c.PromptCache == nil {
		return true
	}
	return *c.PromptCache
}

// UserTimeLocation returns the configured timezone for user-facing timestamps.
// Returns the cached location set by Validate(), or resolves from Timezone string.
// Defaults to UTC if Timezone is empty or invalid.
func (c *CompilerConfig) UserTimeLocation() *time.Location {
	if c.resolvedTZ != nil {
		return c.resolvedTZ
	}
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// UserNow returns the current time formatted in RFC3339 using the configured timezone.
func (c *CompilerConfig) UserNow() string {
	return time.Now().In(c.UserTimeLocation()).Format(time.RFC3339)
}

// Load reads and parses a config file, expanding environment variables.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}

	// Expand environment variables in ${VAR} format
	expanded := expandEnvVars(string(data))

	cfg := Defaults()
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("config.Load: parse error: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save writes the config to a YAML file.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config.Save: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// Validate checks required fields and values.
func (c *Config) Validate() error {
	if c.Project == "" {
		return fmt.Errorf("config: 'project' is required")
	}
	if c.Output == "" {
		return fmt.Errorf("config: 'output' is required")
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("config: at least one source is required")
	}
	if c.API.Provider != "" {
		validProviders := map[string]bool{
			"anthropic": true, "openai": true, "gemini": true, "ollama": true, "openai-compatible": true,
		}
		if !validProviders[c.API.Provider] {
			return fmt.Errorf("config: invalid provider %q (valid: anthropic, openai, gemini, ollama, openai-compatible)", c.API.Provider)
		}
	}
	if c.Serve.Transport != "" {
		if c.Serve.Transport != "stdio" && c.Serve.Transport != "sse" {
			return fmt.Errorf("config: invalid transport %q (valid: stdio, sse)", c.Serve.Transport)
		}
	}
	if c.Compiler.Mode != "" {
		validModes := map[string]bool{"standard": true, "batch": true, "auto": true}
		if !validModes[c.Compiler.Mode] {
			return fmt.Errorf("config: invalid compiler.mode %q (valid: standard, batch, auto)", c.Compiler.Mode)
		}
	}
	// Merge relation_types (preferred) and relations (deprecated) keys.
	// If both are set, relation_types takes precedence.
	if len(c.Ontology.RelationTypes) > 0 {
		c.Ontology.Relations = c.Ontology.RelationTypes
		c.Ontology.RelationTypes = nil // normalize to single field
	} else if len(c.Ontology.Relations) > 0 {
		log.Println("config: ontology.relations is deprecated, use ontology.relation_types instead")
	}
	for _, r := range c.Ontology.Relations {
		if r.Name == "" {
			return fmt.Errorf("config: ontology.relation_types: name is required")
		}
		if !typeNameRe.MatchString(r.Name) {
			return fmt.Errorf("config: ontology.relation_types: invalid name %q (must match [a-z][a-z0-9_]*)", r.Name)
		}
	}
	for _, et := range c.Ontology.EntityTypes {
		if et.Name == "" {
			return fmt.Errorf("config: ontology.entity_types: name is required")
		}
		if !typeNameRe.MatchString(et.Name) {
			return fmt.Errorf("config: ontology.entity_types: invalid name %q (must match [a-z][a-z0-9_]*)", et.Name)
		}
	}
	if c.Search.ChunkSize != 0 && (c.Search.ChunkSize < 100 || c.Search.ChunkSize > 5000) {
		return fmt.Errorf("config: search.chunk_size must be 100-5000, got %d", c.Search.ChunkSize)
	}
	for i, ts := range c.TypeSignals {
		if ts.Type == "" {
			return fmt.Errorf("config: type_signals[%d]: type is required", i)
		}
		if len(ts.FilenameKeywords) == 0 && len(ts.ContentKeywords) == 0 && ts.Pattern == "" {
			return fmt.Errorf("config: type_signals[%d] (%s): at least one keyword (filename, content, or pattern) is required", i, ts.Type)
		}
		if len(ts.ContentKeywords) > 0 && ts.MinContentHits <= 0 {
			return fmt.Errorf("config: type_signals[%d] (%s): min_content_hits must be > 0 when content_keywords is set", i, ts.Type)
		}
	}
	if c.Compiler.Timezone != "" {
		loc, err := time.LoadLocation(c.Compiler.Timezone)
		if err != nil {
			return fmt.Errorf("config: invalid compiler.timezone %q: %w", c.Compiler.Timezone, err)
		}
		c.Compiler.resolvedTZ = loc
	}
	return nil
}

// IsVaultOverlay returns true if this is a vault overlay project.
func (c *Config) IsVaultOverlay() bool {
	return c.Vault != nil
}

// ResolveOutput returns the absolute output path relative to projectDir.
func (c *Config) ResolveOutput(projectDir string) string {
	if filepath.IsAbs(c.Output) {
		return c.Output
	}
	return filepath.Join(projectDir, c.Output)
}

// ResolveSources returns absolute source paths relative to projectDir.
func (c *Config) ResolveSources(projectDir string) []string {
	paths := make([]string, len(c.Sources))
	for i, s := range c.Sources {
		if filepath.IsAbs(s.Path) {
			paths[i] = s.Path
		} else {
			paths[i] = filepath.Join(projectDir, s.Path)
		}
	}
	return paths
}

// expandEnvVars replaces ${VAR} references with environment variable values.
func expandEnvVars(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.Index(s[i:], "}")
			if end != -1 {
				varName := s[i+2 : i+end]
				result.WriteString(os.Getenv(varName))
				i += end + 1
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}
