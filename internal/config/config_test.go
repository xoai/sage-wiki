package config

import (
	"fmt"
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
