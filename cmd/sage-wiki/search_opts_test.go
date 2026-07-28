package main

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

// V-M1a: CLI search must hand the configured hybrid weights to the searcher,
// not the zero values that fall back to 1.0/1.0 inside hybrid.Search.
func TestCLISearchOptsUsesConfigWeights(t *testing.T) {
	cfg := &config.Config{Search: config.SearchConfig{
		HybridWeightBM25:   0.7,
		HybridWeightVector: 0.3,
	}}

	opts := cliSearchOpts(cfg, "compile pipeline", []string{"go"}, 5)

	if opts.BM25Weight != 0.7 {
		t.Errorf("BM25Weight = %v, want 0.7 (config value must be applied)", opts.BM25Weight)
	}
	if opts.VectorWeight != 0.3 {
		t.Errorf("VectorWeight = %v, want 0.3 (config value must be applied)", opts.VectorWeight)
	}
	if opts.Query != "compile pipeline" || opts.Limit != 5 || len(opts.Tags) != 1 {
		t.Errorf("query/tags/limit not carried through: %+v", opts)
	}
}

// Config-load failure keeps the documented BM25-only degrade: zero weights
// let hybrid.Search apply its own 1.0/1.0 defaults, matching today's shape.
func TestCLISearchOptsNilConfigKeepsDefaults(t *testing.T) {
	opts := cliSearchOpts(nil, "q", nil, 0)

	if opts.BM25Weight != 0 || opts.VectorWeight != 0 {
		t.Errorf("nil config must leave weights zero for hybrid defaults, got %+v", opts)
	}
}

// The --scope flag was parsed and never read (dead surface); it is removed
// rather than wired (plan T1.1, review finding F-024).
func TestSearchCmdScopeFlagRemoved(t *testing.T) {
	if f := searchCmd.Flags().Lookup("scope"); f != nil {
		t.Errorf("--scope is still registered on the search command; it is dead surface and must be removed")
	}
}
