package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/prompts"
)

// TestReExtractOptionWithPrompts verifies the variadic option wires a
// per-workspace registry into the run (F-019 disposition).
func TestReExtractOptionWithPrompts(t *testing.T) {
	r := prompts.NewRegistry()
	var ro reExtractOpts
	WithPrompts(r)(&ro)
	if ro.prompts != r {
		t.Error("WithPrompts did not set the registry")
	}
	// nil default keeps the package-default path
	var ro2 reExtractOpts
	if ro2.prompts != nil {
		t.Error("default reExtractOpts must have nil prompts (package default)")
	}
}
