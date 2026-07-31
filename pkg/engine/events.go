package engine

import (
	"context"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/pkg/events"
)

// usageFanOut multiplies a usage recorder: the workspace file ledger always
// gets the event; an installed event sink gets the bridged copy.
type usageFanOut struct {
	file *llm.FileRecorder
	sink events.Sink
	dir  string
}

func (f *usageFanOut) RecordUsage(ctx context.Context, ev llm.UsageEvent) {
	f.file.RecordUsage(ctx, ev)
	if f.sink != nil {
		f.sink.Emit(bridgeUsageEvent(f.dir, ev))
	}
}

// bridgeUsageEvent maps the internal usage ledger event onto the public
// events envelope. Cost converts to float64 dollars (nil stays nil —
// unknown is never a fabricated zero).
func bridgeUsageEvent(dir string, ev llm.UsageEvent) events.Event {
	out := events.Event{
		Kind:             events.KindUsage,
		TS:               ev.TS,
		Workspace:        dir,
		Pass:             ev.Pass,
		Provider:         ev.Provider,
		Model:            ev.Model,
		Tier:             ev.Tier,
		InputTokens:      ev.InputTokens,
		CachedTokens:     ev.CachedTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		OutputTokens:     ev.OutputTokens,
		PriceSource:      ev.PriceSource,
	}
	out.Cost = ev.Cost // decimal end to end — nil when unknown
	return out
}

// usageRecorder builds the compile-time recorder for this Workspace.
func (w *Workspace) usageRecorder() llm.UsageRecorder {
	file := llm.NewFileRecorder(w.dir)
	if w.opts.sink == nil {
		return file
	}
	return &usageFanOut{file: file, sink: w.opts.sink, dir: w.dir}
}
