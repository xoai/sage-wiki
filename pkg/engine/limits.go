package engine

import (
	"github.com/xoai/sage-wiki/internal/limits"
)

// SPEC-08 D1: pkg/engine re-exports the limits model so external callers
// never import internal/. Aliases keep a single implementation (memchain
// ground rule 2) and preserve errors.Is back-compat by variable identity.

// LimitError is the typed error every limit violation returns.
type LimitError = limits.LimitError

// Limits holds the enforceable resource caps (see internal/limits for the
// field semantics).
type Limits = limits.Limits

// Sentinel aliases — engine.ErrDocTooLarge existed before SPEC-08; it is
// now the internal/limits variable, so existing errors.Is call sites keep
// working unchanged.
var (
	ErrDocTooLarge      = limits.ErrDocTooLarge
	ErrBatchTooLarge    = limits.ErrBatchTooLarge
	ErrQueryTooLarge    = limits.ErrQueryTooLarge
	ErrTraversalTooWide = limits.ErrTraversalTooWide
	ErrEncoding         = limits.ErrEncoding
	ErrTimeout          = limits.ErrTimeout
	ErrInvalidName      = limits.ErrInvalidName
)

// Default re-exports — external callers cannot import internal/limits, so
// the documented defaults are part of the engine API.
const (
	DefaultMaxDocBytes                  = limits.DefaultMaxDocBytes
	DefaultMaxDocsPerCaptureBatch       = limits.DefaultMaxDocsPerCaptureBatch
	DefaultMaxCompileBatch              = limits.DefaultMaxCompileBatch
	DefaultMaxQueryBytes                = limits.DefaultMaxQueryBytes
	DefaultMaxGraphTraversalNodes       = limits.DefaultMaxGraphTraversalNodes
	DefaultMaxConcurrentProviderCalls   = limits.DefaultMaxConcurrentProviderCalls
	DefaultMaxConcurrentRequestsPerConn = limits.DefaultMaxConcurrentRequestsPerConn

	DefaultProviderTimeout   = limits.DefaultProviderTimeout
	DefaultCompileDocTimeout = limits.DefaultCompileDocTimeout
)

// NewLimitError constructs a LimitError at enforcement sites.
func NewLimitError(which string, limit, got int64) *LimitError {
	return limits.New(which, limit, got)
}

// WithLimits overrides the workspace limits per-caller. Only non-zero
// fields override; the rest stay at the workspace-config (or default)
// values. An explicit option wins over config on a per-field basis
// (spec D1) — callers can raise OR lower a cap. The hard edge caps
// (MCP wiki_capture 100 KB, REST /v1/capture, serve 1 MiB body caps)
// are separate and never loosen regardless of WithLimits (spec D1
// layering rule).
//
// Scope: honored by Capture, Search, and Graph-Neighbors. The
// compile-path limits (max_compile_batch, compile_doc_timeout,
// max_concurrent_provider_calls, provider_timeout) are read from the
// workspace config — the compiler builds its own client and controllers
// from cfg, so this per-caller override does not reach them. Set those
// via the workspace limits: block for compile runs.
func WithLimits(l Limits) Option {
	return func(o *options) { o.limitsOverride = l }
}

// ResolvedLimits returns the effective limits for this workspace: the
// workspace config's limits block with the WithLimits override applied
// field-by-field, then zero fields resolved to the documented defaults.
func (w *Workspace) ResolvedLimits() Limits {
	base := w.app.Config.Limits
	ov := w.opts.limitsOverride
	if ov.MaxDocBytes != 0 {
		base.MaxDocBytes = ov.MaxDocBytes
	}
	if ov.MaxDocsPerCaptureBatch != 0 {
		base.MaxDocsPerCaptureBatch = ov.MaxDocsPerCaptureBatch
	}
	if ov.MaxCompileBatch != 0 {
		base.MaxCompileBatch = ov.MaxCompileBatch
	}
	if ov.MaxQueryBytes != 0 {
		base.MaxQueryBytes = ov.MaxQueryBytes
	}
	if ov.MaxGraphTraversalNodes != 0 {
		base.MaxGraphTraversalNodes = ov.MaxGraphTraversalNodes
	}
	if ov.MaxConcurrentProviderCalls != 0 {
		base.MaxConcurrentProviderCalls = ov.MaxConcurrentProviderCalls
	}
	if ov.MaxConcurrentRequestsPerConn != 0 {
		base.MaxConcurrentRequestsPerConn = ov.MaxConcurrentRequestsPerConn
	}
	if ov.ProviderTimeout != 0 {
		base.ProviderTimeout = ov.ProviderTimeout
	}
	if ov.CompileDocTimeout != 0 {
		base.CompileDocTimeout = ov.CompileDocTimeout
	}
	return base.Resolve()
}
