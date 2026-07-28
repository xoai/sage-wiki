package config

import (
	"strings"
	"testing"
)

func TestChunkOverlapOrDefault(t *testing.T) {
	// Default 0 — historical chunking, byte-identical (spec §2.5).
	if got := (SearchConfig{}).ChunkOverlapOrDefault(); got != 0 {
		t.Errorf("zero value should default to 0, got %d", got)
	}
	if got := (SearchConfig{ChunkOverlapTokens: 80}).ChunkOverlapOrDefault(); got != 80 {
		t.Errorf("explicit value should be returned, got %d", got)
	}
	if got := (SearchConfig{ChunkOverlapTokens: -5}).ChunkOverlapOrDefault(); got != 0 {
		t.Errorf("negative value should resolve to 0 (off), got %d", got)
	}
}

func TestChunkOverlapValidation(t *testing.T) {
	tests := []struct {
		name      string
		chunkSize int
		overlap   int
		wantErr   string
	}{
		{"unset is valid", 0, 0, ""},
		{"recommended opt-in", 0, 80, ""},              // against the 800 default
		{"at half the default chunk size", 0, 400, ""}, // boundary is inclusive
		{"above half the default chunk size", 0, 401, "must be <= half"},
		{"scaled to an explicit chunk size", 200, 100, ""},
		{"above half an explicit chunk size", 200, 150, "must be <= half"},
		{"negative", 0, -5, "must be >= 0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validBaseConfig()
			c.Search.ChunkSize = tt.chunkSize
			c.Search.ChunkOverlapTokens = tt.overlap

			err := c.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// validBaseConfig is the minimum config that passes Validate, so each case
// isolates the chunk-overlap rule.
func validBaseConfig() *Config {
	return &Config{
		Version: 1,
		Project: "test",
		Output:  "wiki",
		Sources: []Source{{Path: "notes", Type: "md"}},
		API:     APIConfig{Provider: "anthropic", APIKey: "sk-test"},
		Models:  ModelsConfig{Summarize: "m", Extract: "m", Write: "m", Lint: "m", Query: "m"},
	}
}
