package serve

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/linter"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
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

func (d *Deps) Close() {
	d.closeOnce.Do(func() {
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
