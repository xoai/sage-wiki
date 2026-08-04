package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/mirror"
	"github.com/xoai/sage-wiki/pkg/events"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// executeWithShipPass runs the root command and ALWAYS fires the ship pass
// afterward — success or error (AC-9(c-i): the mutation-then-error path
// ships too). main() calls this; tests substitute rootCmd.
func executeWithShipPass() error {
	err := rootCmd.Execute()
	maybeShipAfterCommand()
	return err
}

// maybeShipAfterCommand is the CLI ship pass (spec.md §Components): after
// EVERY command — success or error — if mirror.enabled, run a best-effort
// ship pass. No mutation registry and no cobra hooks: detection is by diff,
// so the pass is a cheap no-op when nothing changed. It NEVER changes the
// command's exit code — failures warn and defer.
func maybeShipAfterCommand() {
	dir, err := filepath.Abs(projectDir)
	if err != nil {
		return
	}
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil || cfg == nil || !cfg.Mirror.Enabled {
		return // disabled or unconfigured: zero-overhead no-op
	}
	mcfg, err := mirror.ConfigFromYAML(dir, cfg.Mirror)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mirror: ship pass skipped: %v\n", err)
		return
	}
	m, err := mirror.Open(dir, mcfg, mirror.NewDiffChangeSource(dir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "mirror: ship pass skipped: %v\n", err)
		return
	}
	// SPEC-07: the CLI ship pass reports mirror_shipped through a
	// short-lived event plane (file sink when events.enable). Closed
	// before the function returns — nothing outlives the command.
	if cfg.Events.EnabledOrDefault() {
		bus := events.NewBus(context.Background(),
			events.WithBufferSize(cfg.Events.BufferSizeOrDefault()),
			events.WithName(filepath.Base(dir)),
			events.WithOnDrop(func(n int64) {
				metrics.CounterNamed("events_dropped_total").Add(n)
			}),
		)
		_ = bus.AddSink(events.NewJSONLFileSink(filepath.Join(dir, cfg.Events.DirOrDefault())))
		m.SetEventSink(bus)
		defer bus.Close()
	}
	// Bounded budget (F-088): a blackholed bucket must not hold the invoking
	// command — 2× ship_lock_timeout overall, then warn and defer.
	ctx, cancel := context.WithTimeout(context.Background(), 2*mcfg.ShipLockTimeout)
	defer cancel()
	if err := m.Ship(ctx, pkmirror.ChangeBatch{}); err != nil {
		// Expected-quiet (F-117): a pre-db workspace simply has nothing to
		// ship yet — the pinned flow must not print scary noise.
		var nr *mirror.NotReadyError
		if errors.As(err, &nr) {
			return
		}
		fmt.Fprintf(os.Stderr, "mirror: ship pass failed: %v (changes ship on a later pass)\n", err)
	}
}
