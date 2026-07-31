// Package provider defines the LLM/embedding abstraction for the engine's
// public API (SPEC-01). Implementations: the engine's default (adapted
// from the workspace config) or any user-supplied value — tests and
// examples use pkg/provider/providerfake.
package provider

import (
	"context"

	"github.com/shopspring/decimal"
)

// Provider is the engine's LLM + embedding surface. Implementations must
// be safe for concurrent use.
type Provider interface {
	// Complete runs one chat completion and returns content plus the
	// authoritative usage split.
	Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error)
	// Embed embeds a batch of texts (same order in/out).
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Models lists the models the provider knows about, with pricing when
	// known (nil Pricing = unknown, never a guess).
	Models(ctx context.Context) ([]ModelInfo, error)
}

// Message is one chat message.
type Message struct {
	Role    string // "system" | "user" | "assistant"
	Content string
}

// CompleteRequest is one completion call.
type CompleteRequest struct {
	Messages  []Message
	Model     string
	MaxTokens int
	// Tier records the compile tier on usage events; -1 when not
	// compile-scoped (query/expansion).
	Tier int
}

// Usage is the token split for one call. Cached tokens are INCLUDED in
// InputTokens; cache-write tokens are separate.
type Usage struct {
	InputTokens      int
	CachedTokens     int
	CacheWriteTokens int
	OutputTokens     int
}

// CompleteResponse is one completion result.
type CompleteResponse struct {
	Content string
	Model   string
	Usage   Usage
}

// ModelInfo describes one model.
type ModelInfo struct {
	ID      string
	Family  string
	Pricing *Pricing // nil = unknown
}

// Pricing holds per-million-token prices. A nil component is unknown.
type Pricing struct {
	InputPerMTok       *decimal.Decimal
	CachedInputPerMTok *decimal.Decimal
	OutputPerMTok      *decimal.Decimal
}
