package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/pkg/events"
)

// MarshalJSON emits the pinned wire schema with Cost as a JSON number or
// null — NOT shopspring's default quoted string. A custom marshaler
// (rather than the library's MarshalJSONWithoutQuotes global) keeps the
// encoding scoped to this type: importing the package must not change
// decimal JSON encoding for the whole host process (embedding rule).
func (e UsageEvent) MarshalJSON() ([]byte, error) {
	// Shadow struct with the identical schema; only the cost field differs.
	type shadow struct {
		TS               time.Time       `json:"ts"`
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
		Assumptions      []string        `json:"assumptions,omitempty"`
	}
	cost := json.RawMessage("null")
	if e.Cost != nil {
		cost = json.RawMessage(e.Cost.String())
	}
	return json.Marshal(shadow{
		TS: e.TS, Pass: e.Pass, Provider: e.Provider, Model: e.Model,
		Tier: e.Tier, InputTokens: e.InputTokens, CachedTokens: e.CachedTokens,
		CacheWriteTokens: e.CacheWriteTokens, OutputTokens: e.OutputTokens,
		Cost: cost, PriceSource: e.PriceSource, Assumptions: e.Assumptions,
	})
}

// NewBridgedRecorder is the SINGLE usage-recording construction (SPEC-07):
// the workspace file ledger always gets the event, and an installed event
// sink gets the bridged copy (nil sink → plain file ledger). Every entry
// path (engine, serve, CLI, parity harness) builds its recorder here.
func NewBridgedRecorder(projectDir string, sink events.Sink) UsageRecorder {
	file := NewFileRecorder(projectDir)
	if sink == nil {
		return file
	}
	return &bridgedRecorder{file: file, sink: sink, workspace: filepath.Base(projectDir)}
}

// bridgedRecorder multiplies a usage recorder: file ledger + event sink.
type bridgedRecorder struct {
	file      *FileRecorder
	sink      events.Sink
	workspace string
}

func (b *bridgedRecorder) RecordUsage(ctx context.Context, ev UsageEvent) {
	b.file.RecordUsage(ctx, ev)
	b.sink.Emit(bridgeUsageEvent(b.workspace, ev))
}

// bridgeUsageEvent maps the usage-ledger record onto the public typed
// union. Cost stays decimal end to end (nil when unknown — never a
// fabricated zero). Workspace is the NAME, never a path (SPEC-07 privacy).
func bridgeUsageEvent(workspace string, ev UsageEvent) events.Event {
	return events.NewEventAt(ev.TS, workspace, events.TypeUsage, events.Usage{
		Pass:             ev.Pass,
		Provider:         ev.Provider,
		Model:            ev.Model,
		Tier:             ev.Tier,
		InputTokens:      ev.InputTokens,
		CachedTokens:     ev.CachedTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		OutputTokens:     ev.OutputTokens,
		Cost:             ev.Cost,
		PriceSource:      ev.PriceSource,
	})
}

// TierNotCompileScoped marks usage events from non-compile paths
// (query, search expansion) — they have no compile tier.
const TierNotCompileScoped = -1

// UsageEvent is the SPEC-05 usage-ledger record and the SPEC-07 event
// contract. The JSON tags ARE the wire schema (pinned by the cost-report
// golden fixture): Cost is a JSON number or null (null = unknown price,
// never zero); a missing assumptions key reads as an empty list.
type UsageEvent struct {
	TS               time.Time        `json:"ts"` // UTC RFC3339
	Pass             string           `json:"pass"`
	Provider         string           `json:"provider"`
	Model            string           `json:"model"`
	Tier             int              `json:"tier"` // 0..3; TierNotCompileScoped for query/expansion
	InputTokens      int              `json:"input_tokens"`
	CachedTokens     int              `json:"cached_tokens"`
	CacheWriteTokens int              `json:"cache_write_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	Cost             *decimal.Decimal `json:"cost"` // null when unknown
	PriceSource      string           `json:"price_source"`
	Assumptions      []string         `json:"assumptions,omitempty"`
}

// UsageRecorder receives one UsageEvent per LLM call. Implementations must
// be safe for concurrent use. The ctx is reserved for future sinks
// (cancellation/deadlines on remote emission); the file ledger's bounded
// append ignores it.
type UsageRecorder interface {
	RecordUsage(context.Context, UsageEvent)
}

// FileRecorder appends usage events as JSONL to <project>/.sage/usage.jsonl.
// Failure policy (stated): an append failure is logged to stderr and the
// event dropped — the ledger is best-effort telemetry and must never fail
// an LLM call.
type FileRecorder struct {
	path string
	mu   sync.Mutex
}

// NewFileRecorder returns a recorder writing to <projectDir>/.sage/usage.jsonl.
func NewFileRecorder(projectDir string) *FileRecorder {
	return &FileRecorder{path: filepath.Join(projectDir, ".sage", "usage.jsonl")}
}

// Path returns the ledger file path (for tests and the cost command).
func (f *FileRecorder) Path() string { return f.path }

func (f *FileRecorder) RecordUsage(_ context.Context, ev UsageEvent) {
	raw, err := json.Marshal(ev)
	if err != nil {
		log.Warn("usage ledger: marshal event failed — dropping", "error", err)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		log.Warn("usage ledger: create dir failed — dropping event", "path", f.path, "error", err)
		return
	}
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		log.Warn("usage ledger: open failed — dropping event", "path", f.path, "error", err)
		return
	}
	defer fh.Close()
	if _, err := fh.Write(append(raw, '\n')); err != nil {
		log.Warn("usage ledger: append failed — dropping event", "path", f.path, "error", err)
	}
}

// ReadUsageLog parses a usage ledger. A missing file yields zero events and
// no error. Malformed lines (a crash mid-append can truncate the final
// line) are skipped with a stderr warning naming the line number — a
// corrupt ledger degrades, it never bricks cost report.
func ReadUsageLog(path string) ([]UsageEvent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read usage log: %w", err)
	}
	var events []UsageEvent
	line := 0
	for _, l := range splitLines(raw) {
		line++
		if len(l) == 0 {
			continue
		}
		var ev UsageEvent
		if err := json.Unmarshal(l, &ev); err != nil {
			log.Warn("usage ledger: skipping malformed line", "path", path, "line", line, "error", err)
			continue
		}
		events = append(events, ev)
	}
	return events, nil
}

func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}
