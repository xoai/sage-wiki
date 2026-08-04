package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/pkg/engine"
	"github.com/xoai/sage-wiki/pkg/events"
)

// cliEventPlane wires the SPEC-07 CLI audit trail: a bus with the JSONL
// file sink when events.enable (+ stdout tee when events.stdout). Webhooks
// and SSE are serve-mode surfaces and stay out of the CLI. Returns the
// engine options plus a cleanup the caller MUST defer. Config errors
// degrade to "no events" — telemetry never breaks a CLI command.
func cliEventPlane(ctx context.Context, dir string) ([]engine.Option, func()) {
	if ctx == nil {
		ctx = context.Background() // cmd.Context() is nil outside Execute
	}
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil || !cfg.Events.EnabledOrDefault() {
		return nil, func() {}
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
	return []engine.Option{engine.WithEventSink(bus)}, func() { bus.Close() }
}
