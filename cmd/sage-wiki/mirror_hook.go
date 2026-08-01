package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/mirror"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

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
	if err := m.Ship(context.Background(), pkmirror.ChangeBatch{}); err != nil {
		fmt.Fprintf(os.Stderr, "mirror: ship pass failed: %v (changes ship on a later pass)\n", err)
	}
}
