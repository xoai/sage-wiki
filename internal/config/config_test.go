package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Version != 1 {
		t.Errorf("expected version 1, got %d", cfg.Version)
	}
	if cfg.Output != "wiki" {
		t.Errorf("expected output 'wiki', got %q", cfg.Output)
	}
	if cfg.Compiler.MaxParallel != 4 {
		t.Errorf("expected max_parallel 4, got %d", cfg.Compiler.MaxParallel)
	}
	if cfg.Search.HybridWeightBM25 != 0.7 {
		t.Errorf("expected bm25 weight 0.7, got %f", cfg.Search.HybridWeightBM25)
	}
}

func TestLoadGreenfield(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
version: 1
project: test-wiki
description: "Test wiki"
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test-key
models:
  summarize: gpt-4o-mini
  extract: gpt-4o-mini
  write: gpt-4o
  lint: gpt-4o-mini
  query: gpt-4o
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Project != "test-wiki" {
		t.Errorf("expected project 'test-wiki', got %q", cfg.Project)
	}
	if cfg.API.Provider != "openai" {
		t.Errorf("expected provider 'openai', got %q", cfg.API.Provider)
	}
	if cfg.IsVaultOverlay() {
		t.Error("should not be vault overlay")
	}
}

func TestLoadVaultOverlay(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
version: 1
project: my-vault
vault:
  root: .
sources:
  - path: Clippings
    type: article
    watch: true
  - path: Papers
    type: paper
    watch: true
output: _wiki
ignore:
  - Daily Notes
  - Personal
api:
  provider: anthropic
  api_key: sk-ant-test
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if !cfg.IsVaultOverlay() {
		t.Error("should be vault overlay")
	}
	if len(cfg.Sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(cfg.Sources))
	}
	if len(cfg.Ignore) != 2 {
		t.Errorf("expected 2 ignore entries, got %d", len(cfg.Ignore))
	}
}

func TestEnvVarExpansion(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	os.Setenv("TEST_SAGE_KEY", "expanded-key-value")
	defer os.Unsetenv("TEST_SAGE_KEY")

	content := `
version: 1
project: env-test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: ${TEST_SAGE_KEY}
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.API.APIKey != "expanded-key-value" {
		t.Errorf("expected expanded key, got %q", cfg.API.APIKey)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing project",
			cfg:     Config{Output: "wiki", Sources: []Source{{Path: "raw"}}},
			wantErr: "'project' is required",
		},
		{
			name:    "missing output",
			cfg:     Config{Project: "test"},
			wantErr: "'output' is required",
		},
		{
			name:    "no sources",
			cfg:     Config{Project: "test", Output: "wiki"},
			wantErr: "at least one source",
		},
		{
			name:    "invalid provider",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, API: APIConfig{Provider: "invalid"}},
			wantErr: "invalid provider",
		},
		{
			name:    "invalid transport",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Serve: ServeConfig{Transport: "websocket"}},
			wantErr: "invalid transport",
		},
		{
			name:    "invalid timezone",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Compiler: CompilerConfig{Timezone: "Not/A/Zone"}},
			wantErr: "invalid compiler.timezone",
		},
		{
			name: "valid timezone",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Compiler: CompilerConfig{Timezone: "Asia/Shanghai"}},
		},
		{
			name:    "invalid relation name uppercase",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{Relations: []RelationConfig{{Name: "Extends"}}}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid relation name empty",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{Relations: []RelationConfig{{Name: ""}}}},
			wantErr: "name is required",
		},
		{
			name:    "invalid relation name special chars",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{Relations: []RelationConfig{{Name: "has-space"}}}},
			wantErr: "invalid name",
		},
		{
			name: "valid relation names via relations key",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{Relations: []RelationConfig{{Name: "regulates", Synonyms: []string{"regulates"}}, {Name: "implements"}}}},
		},
		{
			name: "valid relation names via relation_types key",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{RelationTypes: []RelationConfig{{Name: "regulates"}, {Name: "implements"}}}},
		},
		{
			name:    "invalid entity type name uppercase",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{EntityTypes: []EntityTypeConfig{{Name: "Concept"}}}},
			wantErr: "invalid name",
		},
		{
			name:    "invalid entity type name empty",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{EntityTypes: []EntityTypeConfig{{Name: ""}}}},
			wantErr: "name is required",
		},
		{
			name: "valid entity type names",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{EntityTypes: []EntityTypeConfig{{Name: "conversation"}, {Name: "decision"}}}},
		},
		{
			name: "valid empty ontology",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Ontology: OntologyConfig{}},
		},
		{
			name: "valid minimal",
			cfg:  Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func TestRelationTypesMergePrecedence(t *testing.T) {
	cfg := Config{
		Project: "test",
		Output:  "wiki",
		Sources: []Source{{Path: "raw"}},
		Ontology: OntologyConfig{
			Relations:     []RelationConfig{{Name: "old_key"}},
			RelationTypes: []RelationConfig{{Name: "new_key"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// relation_types should take precedence
	if len(cfg.Ontology.Relations) != 1 || cfg.Ontology.Relations[0].Name != "new_key" {
		t.Errorf("expected relation_types to override relations, got %v", cfg.Ontology.Relations)
	}
	if len(cfg.Ontology.RelationTypes) != 0 {
		t.Error("expected RelationTypes to be normalized to nil after merge")
	}
}

func TestResolvePaths(t *testing.T) {
	cfg := Config{
		Output:  "wiki",
		Sources: []Source{{Path: "raw"}, {Path: "docs"}},
	}

	output := cfg.ResolveOutput("/home/user/project")
	if output != filepath.Join("/home/user/project", "wiki") {
		t.Errorf("unexpected output path: %s", output)
	}

	sources := cfg.ResolveSources("/home/user/project")
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}
	if sources[0] != filepath.Join("/home/user/project", "raw") {
		t.Errorf("unexpected source path: %s", sources[0])
	}
}

func TestCostConfigFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
version: 1
project: cost-test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: anthropic
  api_key: sk-test
compiler:
  mode: batch
  estimate_before: true
  prompt_cache: false
  batch_threshold: 20
  token_price_per_million: 2.5
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Compiler.Mode != "batch" {
		t.Errorf("expected mode 'batch', got %q", cfg.Compiler.Mode)
	}
	if !cfg.Compiler.EstimateBefore {
		t.Error("expected estimate_before true")
	}
	if cfg.Compiler.PromptCacheEnabled() {
		t.Error("expected prompt_cache disabled")
	}
	if cfg.Compiler.BatchThreshold != 20 {
		t.Errorf("expected batch_threshold 20, got %d", cfg.Compiler.BatchThreshold)
	}
	if cfg.Compiler.TokenPriceOverride != 2.5 {
		t.Errorf("expected token_price 2.5, got %f", cfg.Compiler.TokenPriceOverride)
	}
}

func TestCostConfigDefaults(t *testing.T) {
	cfg := Defaults()
	// prompt_cache defaults to true (nil pointer = true)
	if !cfg.Compiler.PromptCacheEnabled() {
		t.Error("expected prompt_cache enabled by default")
	}
	if cfg.Compiler.Mode != "" {
		t.Errorf("expected empty default mode, got %q", cfg.Compiler.Mode)
	}
}

func TestInvalidCompilerMode(t *testing.T) {
	cfg := Config{
		Project: "test",
		Output:  "wiki",
		Sources: []Source{{Path: "raw"}},
		Compiler: CompilerConfig{Mode: "turbo"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for invalid mode")
	}
}

func TestUserTimeLocation(t *testing.T) {
	// Default (empty) returns UTC
	c1 := CompilerConfig{}
	if c1.UserTimeLocation().String() != "UTC" {
		t.Errorf("expected UTC, got %s", c1.UserTimeLocation())
	}

	// Valid IANA timezone (fresh struct — sync.Once is per-instance)
	c2 := CompilerConfig{Timezone: "Asia/Shanghai"}
	loc := c2.UserTimeLocation()
	if loc.String() != "Asia/Shanghai" {
		t.Errorf("expected Asia/Shanghai, got %s", loc)
	}

	// UserNow should contain the timezone offset
	now := c2.UserNow()
	if now == "" {
		t.Error("UserNow returned empty string")
	}
	// Should NOT end with Z (UTC)
	if now[len(now)-1] == 'Z' {
		t.Error("expected non-UTC timezone offset, got Z")
	}

	// Invalid timezone falls back to UTC
	c3 := CompilerConfig{Timezone: "Invalid/Zone"}
	if c3.UserTimeLocation().String() != "UTC" {
		t.Errorf("expected UTC fallback, got %s", c3.UserTimeLocation())
	}

	// Validate() path: timezone resolved and cached during validation
	cfg := Config{
		Project: "test",
		Output:  "wiki",
		Sources: []Source{{Path: "raw"}},
		Compiler: CompilerConfig{Timezone: "America/New_York"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if cfg.Compiler.UserTimeLocation().String() != "America/New_York" {
		t.Errorf("expected America/New_York after Validate, got %s", cfg.Compiler.UserTimeLocation())
	}
}

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

func TestTypeSignalValidation(t *testing.T) {
	base := Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}}

	tests := []struct {
		name    string
		signals []TypeSignal
		wantErr string
	}{
		{
			name:    "missing type",
			signals: []TypeSignal{{FilenameKeywords: []string{"foo"}}},
			wantErr: "type is required",
		},
		{
			name:    "no keywords at all",
			signals: []TypeSignal{{Type: "regulation"}},
			wantErr: "at least one keyword",
		},
		{
			name:    "content keywords without min_content_hits",
			signals: []TypeSignal{{Type: "regulation", ContentKeywords: []string{"foo"}}},
			wantErr: "min_content_hits must be > 0",
		},
		{
			name:    "valid filename only",
			signals: []TypeSignal{{Type: "regulation", FilenameKeywords: []string{"foo"}}},
		},
		{
			name:    "valid content keywords with min hits",
			signals: []TypeSignal{{Type: "regulation", ContentKeywords: []string{"foo"}, MinContentHits: 1}},
		},
		{
			name:    "valid pattern only",
			signals: []TypeSignal{{Type: "regulation", Pattern: "foo"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.TypeSignals = tt.signals
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
