package events

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// DocCaptured is the payload of TypeDocCaptured. DocID is the
// workspace-relative document identifier (the engine's document key) — a
// relative name, never an absolute path.
type DocCaptured struct {
	DocID       string `json:"doc_id"`
	Bytes       int64  `json:"bytes"`
	ContentHash string `json:"content_hash"`
}

// CompileStarted is the payload of TypeCompileStarted.
type CompileStarted struct {
	JobID    string `json:"job_id"`
	Tier     int    `json:"tier"`
	DocCount int    `json:"doc_count"`
}

// UsageSummary is a per-doc token rollup.
type UsageSummary struct {
	InputTokens      int `json:"input_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	OutputTokens     int `json:"output_tokens"`
}

// CompileDocFinished is the payload of TypeCompileDocFinished. Usage and
// Cost are RESERVED and currently always nil: the cost tracker attributes
// usage per LLM call, not per document, so per-doc attribution does not
// exist to report. Consumers must treat nil as "not recorded", never as
// "zero cost" or "unknown price". Populating them requires per-doc usage
// attribution in the tracker (future work).
type CompileDocFinished struct {
	JobID   string           `json:"job_id"`
	DocID   string           `json:"doc_id"`
	Tier    int              `json:"tier"`
	Skipped bool             `json:"skipped"`
	Usage   *UsageSummary    `json:"usage,omitempty"`
	Cost    *decimal.Decimal `json:"-"`
}

// MarshalJSON emits Cost as a JSON number or null — not shopspring's
// default quoted string (type-scoped; the host process's decimal encoding
// is untouched, SPEC-05 embedding rule).
func (p CompileDocFinished) MarshalJSON() ([]byte, error) {
	type shadow struct {
		JobID   string          `json:"job_id"`
		DocID   string          `json:"doc_id"`
		Tier    int             `json:"tier"`
		Skipped bool            `json:"skipped"`
		Usage   *UsageSummary   `json:"usage,omitempty"`
		Cost    json.RawMessage `json:"cost"`
	}
	return json.Marshal(shadow{
		JobID: p.JobID, DocID: p.DocID, Tier: p.Tier, Skipped: p.Skipped,
		Usage: p.Usage, Cost: costRaw(p.Cost),
	})
}

// CompileTotals rolls a whole compile job up.
type CompileTotals struct {
	Docs             int              `json:"docs"`
	Compiled         int              `json:"compiled"`
	Skipped          int              `json:"skipped"`
	InputTokens      int              `json:"input_tokens"`
	CachedTokens     int              `json:"cached_tokens"`
	CacheWriteTokens int              `json:"cache_write_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	Cost             *decimal.Decimal `json:"-"`
}

// CompileFinished is the payload of TypeCompileFinished. Outcome is one of
// "completed", "failed", "interrupted" (serve-queue stop), "cancelled"
// (request context).
type CompileFinished struct {
	JobID   string        `json:"job_id"`
	Outcome string        `json:"outcome"`
	Totals  CompileTotals `json:"totals"`
}

// MarshalJSON renders Totals.Cost as a JSON number or null.
func (p CompileFinished) MarshalJSON() ([]byte, error) {
	type totalsShadow struct {
		Docs             int             `json:"docs"`
		Compiled         int             `json:"compiled"`
		Skipped          int             `json:"skipped"`
		InputTokens      int             `json:"input_tokens"`
		CachedTokens     int             `json:"cached_tokens"`
		CacheWriteTokens int             `json:"cache_write_tokens"`
		OutputTokens     int             `json:"output_tokens"`
		Cost             json.RawMessage `json:"cost"`
	}
	type shadow struct {
		JobID   string       `json:"job_id"`
		Outcome string       `json:"outcome"`
		Totals  totalsShadow `json:"totals"`
	}
	t := p.Totals
	return json.Marshal(shadow{
		JobID: p.JobID, Outcome: p.Outcome,
		Totals: totalsShadow{
			Docs: t.Docs, Compiled: t.Compiled, Skipped: t.Skipped,
			InputTokens: t.InputTokens, CachedTokens: t.CachedTokens,
			CacheWriteTokens: t.CacheWriteTokens, OutputTokens: t.OutputTokens,
			Cost: costRaw(t.Cost),
		},
	})
}

// EdgeAdded is the payload of TypeEdgeAdded. ValidFrom is nil when the
// store did not record a validity window start.
type EdgeAdded struct {
	EdgeID    string     `json:"edge_id"`
	Relation  string     `json:"relation"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	ValidFrom *time.Time `json:"valid_from"`
}

// EdgeInvalidated is the payload of TypeEdgeInvalidated. Window fields are
// nil when not known at the emission site.
type EdgeInvalidated struct {
	EdgeID    string     `json:"edge_id"`
	Reason    string     `json:"reason"`
	ValidFrom *time.Time `json:"valid_from"`
	ValidTo   *time.Time `json:"valid_to"`
}

// EntityResolved is the payload of TypeEntityResolved.
type EntityResolved struct {
	Canonical  string  `json:"canonical"`
	Alias      string  `json:"alias"`
	Confidence float64 `json:"confidence"`
	Auto       bool    `json:"auto"`
}

// PromotionTriggered is the payload of TypePromotionTriggered. Trigger is
// one of "auto-promote", "stale", "compile-on-demand"; FromTier→ToTier
// disambiguates promotion from demotion.
type PromotionTriggered struct {
	DocID    string `json:"doc_id"`
	FromTier int    `json:"from_tier"`
	ToTier   int    `json:"to_tier"`
	Trigger  string `json:"trigger"`
}

// SearchPerformed is the payload of TypeSearchPerformed. QueryHash is the
// SHA-256 of the normalized query; Query carries the raw text only when
// the workspace opted in (events.raw_queries) — empty otherwise.
type SearchPerformed struct {
	QueryHash   string   `json:"query_hash"`
	Query       string   `json:"query,omitempty"`
	Channels    []string `json:"channels"`
	ResultCount int      `json:"result_count"`
	DurationMS  int64    `json:"duration_ms"`
}

// MirrorShipped is the payload of TypeMirrorShipped.
type MirrorShipped struct {
	Generation int64 `json:"generation"`
	Bytes      int64 `json:"bytes"`
}

// MirrorSnapshot is the payload of TypeMirrorSnapshot.
type MirrorSnapshot struct {
	Generation int64 `json:"generation"`
	Bytes      int64 `json:"bytes"`
}

// Usage is the payload of TypeUsage — it mirrors llm.UsageEvent's pinned
// wire schema (SPEC-05): token split, cost-or-nil, price provenance.
type Usage struct {
	Pass             string           `json:"pass"`
	Provider         string           `json:"provider"`
	Model            string           `json:"model"`
	Tier             int              `json:"tier"`
	InputTokens      int              `json:"input_tokens"`
	CachedTokens     int              `json:"cached_tokens"`
	CacheWriteTokens int              `json:"cache_write_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	Cost             *decimal.Decimal `json:"-"`
	PriceSource      string           `json:"price_source"`
}

// MarshalJSON emits Cost as a JSON number or null (SPEC-05 wire rule).
func (p Usage) MarshalJSON() ([]byte, error) {
	type shadow struct {
		Pass             string          `json:"pass"`
		Provider         string          `json:"provider"`
		Model            string          `json:"model"`
		Tier             int             `json:"tier"`
		InputTokens      int             `json:"input_tokens"`
		CachedTokens     int             `json:"cached_tokens"`
		CacheWriteTokens int             `json:"cache_write_tokens"`
		OutputTokens     int             `json:"output_tokens"`
		Cost             json.RawMessage `json:"cost"`
		PriceSource      string          `json:"price_source"`
	}
	return json.Marshal(shadow{
		Pass: p.Pass, Provider: p.Provider, Model: p.Model, Tier: p.Tier,
		InputTokens: p.InputTokens, CachedTokens: p.CachedTokens,
		CacheWriteTokens: p.CacheWriteTokens, OutputTokens: p.OutputTokens,
		Cost: costRaw(p.Cost), PriceSource: p.PriceSource,
	})
}

// CompileSkip is the payload of TypeCompileSkip. DocID is the
// workspace-relative document identifier.
type CompileSkip struct {
	DocID  string `json:"doc_id"`
	Reason string `json:"reason"` // "unchanged" | "unchanged (adopted)"
}

// EventsDropped is the payload of TypeEventsDropped: how many events were
// dropped since the last emission (coalesced — one event per overflow
// episode, never one per drop).
type EventsDropped struct {
	Dropped int64 `json:"dropped"`
}

// LimitExceeded is the payload of TypeLimitExceeded (SPEC-08): which limit
// fired, its configured value, and the offending value. Detail carries a
// short locator (doc id / path fragment / query hash) — never full content.
type LimitExceeded struct {
	Which  string `json:"which"`
	Limit  int64  `json:"limit"`
	Got    int64  `json:"got"`
	Detail string `json:"detail,omitempty"`
}

// EdgeRejected is the payload of TypeEdgeRejected (SPEC-08): an LLM-emitted
// edge dropped before persist. Allowed Reason values are pinned in spec D2
// ("span_missing" this cycle).
type EdgeRejected struct {
	Source    string `json:"source"`
	Predicate string `json:"predicate"`
	Target    string `json:"target"`
	Reason    string `json:"reason"`
}

// costRaw renders a decimal cost as a raw JSON number, or null when nil.
func costRaw(c *decimal.Decimal) json.RawMessage {
	if c == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(c.String())
}
