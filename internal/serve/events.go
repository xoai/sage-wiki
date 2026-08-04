package serve

import (
	"context"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/pkg/events"
)

// BuildEventSurfaces constructs the SPEC-07 event plane for ONE workspace:
// bus + audit-trail JSONL file sink (+ stdout tee + webhook dispatchers).
// Returns (nil, nil, nil) when events are disabled or the config is
// unavailable — every consumer is nil-safe. Webhook config errors fail
// startup (fail loudly, never at first delivery). The caller owns the
// teardown order: bus.Close() FIRST (drains events into the sinks), then
// the stops (the dispatcher dead-letters its residue).
func BuildEventSurfaces(ctx context.Context, dir string, cfg *config.Config) (*events.Bus, []func(), error) {
	if cfg == nil || !cfg.Events.EnabledOrDefault() {
		return nil, nil, nil
	}
	bus := events.NewBus(ctx,
		events.WithBufferSize(cfg.Events.BufferSizeOrDefault()),
		events.WithName(filepath.Base(dir)),
		events.WithOnDrop(func(n int64) {
			metrics.CounterNamed("events_dropped_total").Add(n)
		}),
	)
	_ = bus.AddSink(events.NewJSONLFileSink(filepath.Join(dir, cfg.Events.DirOrDefault())))
	if cfg.Events.Stdout {
		_ = bus.AddSink(events.NewWriterSink(os.Stdout))
	}
	var stops []func()
	var firstErr error
	if len(cfg.Serve.Webhooks) > 0 {
		d, err := NewWebhookDispatcher(ctx, dir, cfg.Serve.Webhooks)
		if err != nil {
			// The bus survives a webhook failure — the audit-trail file
			// sink must not die with the dispatcher. The error propagates:
			// single-mode serve fails startup with it (fail loudly, SPEC-07
			// §5); multi-mode degrades per workspace (recorded deviation).
			firstErr = err
		} else {
			_ = bus.AddSink(d)
			stops = append(stops, d.Stop)
		}
	}
	return bus, stops, firstErr
}
