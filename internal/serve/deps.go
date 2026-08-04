package serve

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/linter"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/mirror"
	"github.com/xoai/sage-wiki/pkg/events"
)

// serveDeps assembles the shared serve-mode compile state (P2-3, spec C4):
// ONE coordinator serializing MCP/on-demand compiles with the queue worker,
// ONE progress hub both feed, and the worker itself (nil when disabled).
type Deps struct {
	coord     *compiler.CompileCoordinator
	progress  *compiler.Progress
	worker    *compiler.Worker
	workerApp *app.App // the worker's own backend handle; nil when disabled
	workerWG  sync.WaitGroup
	closeOnce sync.Once

	mirrorShipper *MirrorShipper // nil unless mirror.enabled
	dir           string         // workspace dir (SPEC-07 sink binding)

	eventSink events.Sink // SPEC-07: workspace event bus; nil = no events
}

// SetEventSink installs the workspace event sink for serve-path compiles
// AND the mirror shipper (SPEC-07 — both bypass pkg/engine, so both thread
// here). The sink is workspace-bound: stores/shippers do not know their
// workspace name.
func (d *Deps) SetEventSink(s events.Sink) {
	s = events.NilSafe(s) // typed-nil guard — see NilSafe
	d.eventSink = s
	if d.mirrorShipper != nil {
		d.mirrorShipper.m.SetEventSink(events.BindWorkspace(s, filepath.Base(d.dir)))
	}
	if d.worker != nil {
		d.worker.SetEventSink(s) // worker cycles join the same plane
	}
}

// Progress returns the shared progress hub.
func (d *Deps) Progress() *compiler.Progress { return d.progress }

// WorkerEnabled reports whether a queue worker is present.
func (d *Deps) WorkerEnabled() bool { return d.worker != nil }

// Coordinator returns the shared compile coordinator.
func (d *Deps) Coordinator() *compiler.CompileCoordinator { return d.coord }

// StartWorker launches the worker goroutine; Close waits for it.
func (d *Deps) StartWorker(ctx context.Context) {
	if d.worker == nil {
		return
	}
	d.workerWG.Add(1)
	go func() {
		defer d.workerWG.Done()
		d.worker.Run(ctx)
	}()
}

// assembleServeDeps builds the shared state. The worker opens its own
// app container (a second handle to the same database — safe: queue
// fencing is DB-level); it is the only component besides the servers.
func AssembleDeps(dir string) (*Deps, error) {
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("serve deps: %w", err)
	}
	d := &Deps{
		coord:    compiler.NewCompileCoordinator(),
		progress: compiler.NewProgress(),
		dir:      dir,
	}
	if !cfg.Serve.WorkerEnabled() {
		assembleMirrorShipper(d, dir, cfg)
		return d, nil
	}
	a, err := app.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("serve deps: worker backend: %w", err)
	}
	d.workerApp = a
	d.worker = compiler.NewWorkerForServe(dir, a.Backend, d.coord, d.progress, cfg.Serve.Worker)
	assembleMirrorShipper(d, dir, cfg)
	return d, nil
}

// assembleMirrorShipper attaches the in-process shipper when mirror.enabled
// (spec.md §Components: serve AND serve --ui share serveDeps). Failures are
// loud warnings, never serve-fatal — mirroring is best-effort.
func assembleMirrorShipper(d *Deps, dir string, cfg *config.Config) {
	if !cfg.Mirror.Enabled {
		return
	}
	mcfg, err := mirror.ConfigFromYAML(dir, cfg.Mirror)
	if err != nil {
		slog.Warn("mirror: shipper disabled (config error)", "err", err)
		return
	}
	mm, err := mirror.Open(dir, mcfg, mirror.NewDiffChangeSource(dir))
	if err != nil {
		slog.Warn("mirror: shipper disabled (open error)", "err", err)
		return
	}
	d.mirrorShipper = NewMirrorShipper(mm, mcfg)
}

// StartMirror launches the mirror shipper when enabled.
func (d *Deps) StartMirror(ctx context.Context) {
	if d.mirrorShipper == nil {
		return
	}
	d.mirrorShipper.Start(ctx)
}

func (d *Deps) Close() {
	d.closeOnce.Do(func() {
		// Drain the shipper (final segment within drain_timeout) BEFORE
		// closing the handles it reads from.
		if d.mirrorShipper != nil {
			d.mirrorShipper.Stop()
		}
		// Wait for the in-flight cycle before closing the handle under it.
		d.workerWG.Wait()
		if d.workerApp != nil {
			d.workerApp.Close()
		}
	})
}

// newJobRunner creates a JobRunner backed by serve-mode state. Full compile
// runs against the worker backend; topic-compile and lint delegate to the
// MCP server (P4-2: one execution path — the same wiring the MCP tools use).
func NewJobRunner(d *Deps, mcpSrv *mcppkg.Server) api.JobRunner {
	return &serveJobRunner{deps: d, mcp: mcpSrv}
}

type serveJobRunner struct {
	deps *Deps
	mcp  *mcppkg.Server
}

func (r *serveJobRunner) RunCompile(ctx context.Context, projectDir string, opts compiler.CompileOpts) (*compiler.CompileResult, error) {
	opts.Ctx = ctx
	opts.Progress = r.deps.progress
	if opts.Sink == nil {
		opts.Sink = r.deps.eventSink
	}
	if r.deps.workerApp != nil {
		opts.Backend = r.deps.workerApp.Backend
	}
	var result *compiler.CompileResult
	var err error
	acquired, _ := r.deps.coord.TryCompile(func() error {
		result, err = compiler.Compile(projectDir, opts)
		return err
	})
	if !acquired {
		return nil, fmt.Errorf("compile already in progress — another compile holds the coordinator lock")
	}
	return result, err
}

func (r *serveJobRunner) RunCompileTopic(ctx context.Context, opts compiler.OnDemandOpts) (*compiler.OnDemandResult, error) {
	if r.mcp == nil {
		return nil, fmt.Errorf("compile-on-demand not configured")
	}
	return r.mcp.CompileTopic(ctx, opts.Topic, opts.MaxSources)
}

func (r *serveJobRunner) RunLint(ctx context.Context, lintCtx *linter.LintContext, passName string, fix bool) ([]linter.LintResult, error) {
	if r.mcp == nil {
		return nil, fmt.Errorf("lint not configured")
	}
	return r.mcp.RunLint(passName, fix)
}

// Ensure serveJobRunner implements api.JobRunner.
var _ api.JobRunner = (*serveJobRunner)(nil)
