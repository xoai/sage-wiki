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
	if cfg.Embed == nil {
		t.Fatal("expected default embed config")
	}
	if cfg.Embed.Provider != "auto" {
		t.Errorf("expected default embed provider 'auto', got %q", cfg.Embed.Provider)
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
			name:    "invalid embed provider",
			cfg:     Config{Project: "test", Output: "wiki", Sources: []Source{{Path: "raw"}}, Embed: &EmbedConfig{Provider: "bad-provider"}},
			wantErr: "invalid embed provider",
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
