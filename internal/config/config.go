package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the sage-wiki project configuration.
type Config struct {
	Version     int            `yaml:"version"`
	Project     string         `yaml:"project"`
	Description string         `yaml:"description"`
	Vault       *VaultConfig   `yaml:"vault,omitempty"`
	Sources     []Source       `yaml:"sources"`
	Output      string         `yaml:"output"`
	Ignore      []string       `yaml:"ignore,omitempty"`
	API         APIConfig      `yaml:"api"`
	Models      ModelsConfig   `yaml:"models"`
	Embed       *EmbedConfig   `yaml:"embed,omitempty"`
	Compiler    CompilerConfig `yaml:"compiler"`
	Search      SearchConfig   `yaml:"search"`
	Linting     LintingConfig  `yaml:"linting"`
	Serve       ServeConfig    `yaml:"serve"`
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
}

type CompilerConfig struct {
	MaxParallel      int  `yaml:"max_parallel"`
	DebounceSeconds  int  `yaml:"debounce_seconds"`
	SummaryMaxTokens int  `yaml:"summary_max_tokens"`
	ArticleMaxTokens int  `yaml:"article_max_tokens"`
	AutoCommit       bool `yaml:"auto_commit"`
	AutoLint         bool `yaml:"auto_lint"`
}

type SearchConfig struct {
	HybridWeightBM25   float64 `yaml:"hybrid_weight_bm25"`
	HybridWeightVector float64 `yaml:"hybrid_weight_vector"`
	DefaultLimit       int     `yaml:"default_limit"`
}

type LintingConfig struct {
	AutoFixPasses          []string `yaml:"auto_fix_passes"`
	StalenessThresholdDays int      `yaml:"staleness_threshold_days"`
}

type ServeConfig struct {
	Transport string `yaml:"transport"`
	Port      int    `yaml:"port"`
}

// Defaults returns a Config with sensible defaults for greenfield mode.
func Defaults() Config {
	return Config{
		Version: 1,
		Output:  "wiki",
		Sources: []Source{{Path: "raw", Type: "auto", Watch: true}},
		Embed: &EmbedConfig{
			Provider: "auto",
			Model:    "",
		},
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
	if c.Embed != nil && c.Embed.Provider != "" {
		validEmbedProviders := map[string]bool{
			"auto": true, "openai": true, "gemini": true, "ollama": true, "openai-compatible": true, "voyage": true, "mistral": true,
		}
		if !validEmbedProviders[c.Embed.Provider] {
			return fmt.Errorf("config: invalid embed provider %q (valid: auto, openai, gemini, ollama, openai-compatible, voyage, mistral)", c.Embed.Provider)
		}
	}
	if c.Serve.Transport != "" {
		if c.Serve.Transport != "stdio" && c.Serve.Transport != "sse" {
			return fmt.Errorf("config: invalid transport %q (valid: stdio, sse)", c.Serve.Transport)
		}
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
