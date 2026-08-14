package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/xoai/sage-wiki/internal/app"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/providerutil"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
	"github.com/xoai/sage-wiki/pkg/provider"
)

// Sentinel errors of the engine API. All are wrapped with context;
// errors.Is works through the wrapping.
var (
	// ErrIncompatibleVersion reports a mutation attempted on a pre-format
	// (v0.2.x) workspace opened without WithUpgrade.
	ErrIncompatibleVersion = errors.New("engine: workspace predates format versioning — open with WithUpgrade to adopt it")
	// ErrDocTooLarge lives in limits.go (SPEC-08 alias to internal/limits).
	// ErrBudgetExceeded reports a compile stopped by CompileRequest.MaxCost.
	ErrBudgetExceeded = errors.New("engine: compile stopped at MaxCost")
	// ErrNotInitialized reports Open on a directory that is not a workspace.
	ErrNotInitialized = errors.New("engine: not a sage-wiki workspace (no config.yaml)")
	// ErrConfigLoad wraps config-load failures so CLI shims can errors.Is
	// exactly that case (the runSearch BM25-degrade fallback).
	ErrConfigLoad = errors.New("engine: config load failed")
	// ErrReadOnly reports a mutation attempted on a read-only Workspace.
	ErrReadOnly = errors.New("engine: workspace opened read-only")
)

// Option customizes Open/Init.
type Option func(*options)

type options struct {
	configFile      string
	provider        provider.Provider
	compileProvider provider.Provider
	sink            events.Sink
	logger          *slog.Logger
	upgrade         bool
	readOnly        bool
	limitsOverride  limits.Limits
}

// WithConfigFile loads config from an explicit path instead of
// <dir>/config.yaml.
func WithConfigFile(path string) Option {
	return func(o *options) { o.configFile = path }
}

// WithProvider injects an embedding provider for the vector search legs
// (tests, examples, self-hosters' custom backends). Default: built from
// the workspace config. v1 SCOPE: the provider feeds search embeddings
// only; compile and expand/rerank LLM calls remain config-driven unless
// callers separately opt into WithCompileProvider for compilation. p must
// be non-nil; the existing typed-nil behavior is retained for compatibility.
func WithProvider(p provider.Provider) Option {
	return func(o *options) { o.provider = p }
}

// WithCompileProvider injects a completion provider for Workspace.Compile.
// It is independent from WithProvider, which remains search-embedding-only.
// Workspace config still supplies models, prompts, tiers, limits, paths, and
// embedding settings. The caller-owned provider controls completion retries,
// rate limits, and temperature; failures never fall back to config credentials.
// Injected compilation is synchronous-only, and unknown prices keep MaxCost
// inactive unless the injected identity is explicitly priced. Nil and
// typed-nil values normalize to the existing config-backed behavior.
func WithCompileProvider(p provider.Provider) Option {
	return func(o *options) {
		if providerutil.IsNil(p) {
			o.compileProvider = nil
			return
		}
		o.compileProvider = p
	}
}

// WithEventSink subscribes to engine events (usage events today; SPEC-07
// widens the union). A typed-nil sink (e.g. a nil *Bus passed under
// events.enable=false) normalizes to plain nil — the workspace degrades
// event-free instead of panicking on the first Emit.
func WithEventSink(s events.Sink) Option {
	return func(o *options) { o.sink = events.NilSafe(s) }
}

// WithLogger routes engine diagnostics to the given logger instead of the
// process-global default.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// WithUpgrade adopts a pre-format (v0.2.x) workspace: the manifest is
// stamped with the current format (one-way) and mutations are enabled.
// Without it, a pre-format workspace opens read-only and mutators return
// ErrIncompatibleVersion.
func WithUpgrade() Option {
	return func(o *options) { o.upgrade = true }
}

// WithReadOnly opens without the exclusive lock (reads never contend with
// a writer — sqlite WAL keeps them consistent). Mutators return
// ErrReadOnly.
func WithReadOnly() Option {
	return func(o *options) { o.readOnly = true }
}

// Workspace is one open workspace. It is NOT safe to copy; always Close.
//
// Concurrency: Search, Graph, and Stats take the read lock and may run
// concurrently; Capture and Compile take the write lock — one mutating
// goroutine at a time (SPEC-01 single-writer invariant, in-process half;
// the file lock is the cross-process half).
type Workspace struct {
	dir       string
	opts      options
	app       *app.App
	lock      *workspaceLock
	prompts   *prompts.Registry
	preFormat bool

	mu     sync.RWMutex
	closed bool

	dimsOnce sync.Once // provider-embedding dimension probe cache
	dims     int
}

// orBackground normalizes a nil context (cobra's cmd.Context() is nil
// outside Execute).
func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Open validates the directory, checks the workspace format, acquires the
// exclusive lock (unless WithReadOnly), and returns the handle. A second
// read-write Open of the same dir fails fast with ErrLocked.
func Open(ctx context.Context, dir string, optFns ...Option) (*Workspace, error) {
	ctx = orBackground(ctx)
	var opts options
	for _, fn := range optFns {
		fn(&opts)
	}
	if opts.upgrade && opts.readOnly {
		return nil, fmt.Errorf("engine: WithUpgrade requires a read-write open (drop WithReadOnly) — adoption is a mutation")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve dir: %w", err)
	}
	configPath := opts.configFile
	if configPath == "" {
		configPath = filepath.Join(abs, "config.yaml")
	}
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("engine: stat config: %w", err)
	}

	// Format check BEFORE opening storage: a pre-format workspace opens
	// read-only (no migrations) unless adopted via WithUpgrade.
	manifestPath := filepath.Join(abs, ".manifest.json")
	mf, err := manifest.Load(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("engine: load workspace manifest: %w", err)
	}
	mf.SetNow(config.NowUTC)
	preFormat := mf.IsPreFormat()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	w := &Workspace{dir: abs, opts: opts, preFormat: preFormat}

	// Lock: read-write opens only — never for a pre-format workspace
	// (read-only anyway), but ALWAYS before an adoption stamp so two
	// concurrent --upgrade opens serialize on the lock, not on luck (B-02).
	needsLock := !opts.readOnly && (!preFormat || opts.upgrade)
	if needsLock {
		lock, err := acquireLock(abs)
		if err != nil {
			return nil, err
		}
		w.lock = lock
	}

	if preFormat && opts.upgrade {
		mf.FormatVersion = manifest.CurrentFormatVersion
		mf.Engine = manifest.EngineVersion
		if err := mf.Save(manifestPath); err != nil {
			w.lock.release()
			return nil, fmt.Errorf("engine: adopt workspace format: %w", err)
		}
		preFormat = false
		w.preFormat = false
	}

	mode := store.ModeWriter
	if opts.readOnly || preFormat {
		mode = store.ModeReader
	}
	a, err := app.OpenWithOptions(abs, configPath, mode)
	if err != nil {
		if w.lock != nil {
			w.lock.release()
		}
		var cfgErr *app.ConfigLoadError
		if errors.As(err, &cfgErr) {
			return nil, fmt.Errorf("%w: %v", ErrConfigLoad, cfgErr)
		}
		return nil, err
	}
	w.app = a

	w.prompts = prompts.NewRegistry()
	// Unparseable overrides warn and fall back to embedded defaults — the
	// same degrade the compiler always had (Open must not hard-fail where
	// compile/search previously continued).
	if err := w.prompts.LoadFromDir(filepath.Join(abs, "prompts")); err != nil {
		if opts.logger != nil {
			opts.logger.Warn("prompt overrides failed to load, using defaults", "error", err)
		} else {
			fmt.Fprintf(os.Stderr, "warning: prompt overrides failed to load (%v) — using defaults\n", err)
		}
	}

	return w, nil
}

// Init creates a new workspace in dir (greenfield) and opens it.
func Init(ctx context.Context, dir string, optFns ...Option) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve dir: %w", err)
	}
	if err := wiki.InitGreenfield(abs, filepath.Base(abs), ""); err != nil {
		return nil, fmt.Errorf("engine: init workspace: %w", err)
	}
	return Open(ctx, abs, optFns...)
}

// Close flushes and releases everything. Idempotent.
func (w *Workspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var firstErr error
	if w.app != nil {
		if err := w.app.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if w.lock != nil {
		if err := w.lock.release(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// checkOpen guards against use-after-close. Callers MUST hold w.mu (read
// or write) — w.closed is written under the write lock in Close, so an
// unlocked read would race it.
func (w *Workspace) checkOpen() error {
	if w.closed {
		return errors.New("engine: workspace is closed")
	}
	return nil
}

// checkMutable guards mutators on read-only/pre-format workspaces.
// Callers MUST hold w.mu (write) — see checkOpen.
func (w *Workspace) checkMutable() error {
	if err := w.checkOpen(); err != nil {
		return err
	}
	if w.preFormat {
		return ErrIncompatibleVersion
	}
	if w.opts.readOnly {
		return ErrReadOnly
	}
	return nil
}

// Dir returns the workspace's absolute directory.
func (w *Workspace) Dir() string { return w.dir }

// RequiresUpgrade reports whether this workspace predates format
// versioning (v0.2.x) and was therefore opened read-only — mutators
// return ErrIncompatibleVersion until it is re-opened with WithUpgrade.
// CLI shims use this to direct users at the --upgrade flag.
func (w *Workspace) RequiresUpgrade() bool { return w.preFormat }
