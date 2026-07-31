// Package events defines the engine's event sink contract (SPEC-01).
// The typed Event union is wired by SPEC-07; today the engine emits
// usage events (SPEC-05) through this seam.
package events

import (
	"time"

	"github.com/shopspring/decimal"
)

// Sink receives events from a Workspace. Implementations must be safe for
// concurrent use and must not block — a slow sink must not stall the
// engine.
type Sink interface {
	Emit(Event)
}

// Kind identifies the event variant.
type Kind string

const (
	// KindUsage is an LLM usage event (SPEC-05).
	KindUsage Kind = "usage"
)

// Event is the engine's event envelope. Fields not relevant to the Kind
// are zero.
type Event struct {
	Kind      Kind
	TS        time.Time
	Workspace string // absolute workspace dir

	// Usage payload (Kind == KindUsage): mirrors llm.UsageEvent's wire
	// schema — pass/provider/model/tier, token split, cost-or-nil.
	Pass             string
	Provider         string
	Model            string
	Tier             int
	InputTokens      int
	CachedTokens     int
	CacheWriteTokens int
	OutputTokens     int
	Cost             *decimal.Decimal // nil when unknown — never a fabricated zero
	PriceSource      string
}
