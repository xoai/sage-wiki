package api

import (
	"context"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/linter"
)

// JobRunner wraps the long-running compile and lint functions. The
// concrete implementation is wired by cmd/sage-wiki to capture the
// stores, embedder, clients, and coordinator those functions need.
type JobRunner interface {
	RunCompile(ctx context.Context, projectDir string, opts compiler.CompileOpts) (*compiler.CompileResult, error)
	RunCompileTopic(ctx context.Context, opts compiler.OnDemandOpts) (*compiler.OnDemandResult, error)
	RunLint(ctx context.Context, lintCtx *linter.LintContext, passName string, fix bool) ([]linter.LintResult, error)
}
