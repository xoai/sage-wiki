package main

import (
	"fmt"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
)

// serveDeps assembles the shared serve-mode compile state (P2-3, spec C4):
// ONE coordinator serializing MCP/on-demand compiles with the queue worker,
// ONE progress hub both feed, and the worker itself (nil when disabled).
type serveDeps struct {
	coord     *compiler.CompileCoordinator
	progress  *compiler.Progress
	worker    *compiler.Worker
	workerApp *app.App // the worker's own backend handle; nil when disabled
}

// assembleServeDeps builds the shared state. The worker opens its own
// app container (a second handle to the same database — safe: queue
// fencing is DB-level); it is the only component besides the servers.
func assembleServeDeps(dir string) (*serveDeps, error) {
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("serve deps: %w", err)
	}
	d := &serveDeps{
		coord:    compiler.NewCompileCoordinator(),
		progress: compiler.NewProgress(),
	}
	if !cfg.Serve.WorkerEnabled() {
		return d, nil
	}
	a, err := app.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("serve deps: worker backend: %w", err)
	}
	d.workerApp = a
	d.worker = compiler.NewWorkerForServe(dir, a.Backend, d.coord, d.progress, cfg.Serve.Worker)
	return d, nil
}

func (d *serveDeps) Close() {
	if d.workerApp != nil {
		d.workerApp.Close()
	}
}
