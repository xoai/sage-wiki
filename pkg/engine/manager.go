package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/pathsafe"
)

// Manager sentinel errors. errors.Is works through wrapping.
var (
	// ErrUnknownWorkspace reports a valid name with no workspace directory
	// under the Manager's root.
	ErrUnknownWorkspace = errors.New("engine: no such workspace in registry")
	// ErrManagerClosed reports use of a Manager after Close.
	ErrManagerClosed = errors.New("engine: manager is closed")
)

// WorkspaceInfo describes one registry entry.
type WorkspaceInfo struct {
	Name            string
	Dir             string
	Open            bool // currently held open by the Manager
	RequiresUpgrade bool // pre-format v0.2.x workspace (opens read-only)
}

// ManagerOption customizes OpenManager.
type ManagerOption func(*managerOptions)

type managerOptions struct {
	maxOpen int
	// idleClose closes handles unused for longer than this; 0 disables.
	idleClose time.Duration
	// perWorkspaceOptions injects per-workspace Open options — the
	// composition point an operator uses for per-workspace config.
	perWorkspaceOptions func(name string) []Option
	// onEvict, when set, is called INSTEAD of ws.Close on LRU eviction
	// (the Manager owns WHEN; the hook owns HOW — serve stacks use it to
	// wait out request refcounts before closing).
	onEvict func(name string, ws *Workspace) error
}

// WithMaxOpen bounds concurrently open workspaces; the least-recently-used
// handle is evicted (closed) before opening beyond n. 0 = unlimited.
func WithMaxOpen(n int) ManagerOption {
	return func(o *managerOptions) { o.maxOpen = n }
}

// WithIdleClose closes handles idle longer than d. 0 = off.
func WithIdleClose(d time.Duration) ManagerOption {
	return func(o *managerOptions) { o.idleClose = d }
}

// WithPerWorkspaceOptions supplies engine.Open options per workspace name.
func WithPerWorkspaceOptions(fn func(name string) []Option) ManagerOption {
	return func(o *managerOptions) { o.perWorkspaceOptions = fn }
}

// WithOnEvict replaces the eviction close with a hook (nil = ws.Close).
func WithOnEvict(fn func(name string, ws *Workspace) error) ManagerOption {
	return func(o *managerOptions) { o.onEvict = fn }
}

// managerHandle is one open workspace in the Manager's cache.
type managerHandle struct {
	ws      *Workspace
	lastUse time.Time
}

// Manager manages many workspaces in one process: a registry of
// subdirectories of root, lazy open on first use, LRU close beyond
// MaxOpen, optional idle close. Safe for concurrent use.
//
// Synchronization contract: one mutex serializes the handle map — lookups,
// detaches, and the idle-close goroutine never interleave on the map.
// Closes (eviction, idle) run OUTSIDE the mutex so a slow drain never
// stalls unrelated operations. Eviction of a handle with in-flight
// operations BLOCKS on the Workspace's RWMutex until readers drain
// (uncancellable — stdlib RWMutex has no ctx-aware acquisition); a
// perpetually hot workspace therefore cannot be evicted, so MaxOpen
// bounds OPEN handles, not eviction promptness. Mutex acquisition itself
// is uncancellable; ctx is honored inside engine.Open. Callers MUST NOT
// retain handles across Manager calls that may evict; a retained evicted
// handle reports "workspace is closed".
type Manager struct {
	root string
	opts managerOptions

	mu      sync.Mutex
	handles map[string]*managerHandle
	closed  bool

	idleStop chan struct{}
	idleDone chan struct{}
}

// OpenManager validates root and returns the Manager. The background idle
// goroutine (if WithIdleClose) takes ctx and stops on cancel or Close.
func OpenManager(ctx context.Context, root string, optFns ...ManagerOption) (*Manager, error) {
	ctx = orBackground(ctx)
	var opts managerOptions
	for _, fn := range optFns {
		fn(&opts)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("engine: resolve manager root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("engine: manager root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("engine: manager root %s is not a directory", abs)
	}
	m := &Manager{root: abs, opts: opts, handles: map[string]*managerHandle{}}
	if opts.idleClose > 0 {
		m.idleStop = make(chan struct{})
		m.idleDone = make(chan struct{})
		go m.idleLoop(ctx, opts.idleClose)
	}
	return m, nil
}

// Workspace returns the open handle for name, opening it lazily on first
// use. The handle is shared — do NOT retain it across Manager calls that
// may evict (any Workspace call beyond MaxOpen, or idle close).
//
// Concurrency: concurrent first opens of the same name converge on ONE
// handle — losers of the engine.Open race see ErrLocked, wait briefly,
// and re-check the map for the winner. Mutex acquisition (like the
// documented drain) is UNCANCELLABLE; ctx is honored inside engine.Open.
func (m *Manager) Workspace(ctx context.Context, name string) (*Workspace, error) {
	ctx = orBackground(ctx)
	if err := ValidateWorkspaceName(name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrManagerClosed
	}
	if h := m.cached(name); h != nil {
		return h, nil
	}

	// Registry + containment check outside the lock (filesystem work).
	dir, err := m.registryDir(name)
	if err != nil {
		return nil, err
	}
	optFns := []Option{}
	if m.opts.perWorkspaceOptions != nil {
		optFns = m.opts.perWorkspaceOptions(name)
	}

	var ws *Workspace
	for attempt := 0; ; attempt++ {
		ws, err = Open(ctx, dir, optFns...)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrLocked) || attempt >= 20 {
			return nil, err
		}
		// A concurrent opener holds the lock: it may be a Manager caller
		// about to insert the shared handle — re-check before retrying.
		time.Sleep(25 * time.Millisecond)
		if h := m.cached(name); h != nil {
			return h, nil
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if err := ws.Close(); err != nil {
			log.Warn("engine: close of racing workspace open failed", "name", name, "error", err)
		}
		return nil, ErrManagerClosed
	}
	// A concurrent opener may have won the race; close our duplicate.
	if h, ok := m.handles[name]; ok {
		h.lastUse = time.Now()
		m.mu.Unlock()
		if err := ws.Close(); err != nil {
			log.Warn("engine: close of duplicate workspace open failed", "name", name, "error", err)
		}
		return h.ws, nil
	}
	victimName, victim := m.evictCandidateLocked(1)
	if victim != nil {
		delete(m.handles, victimName)
	}
	m.handles[name] = &managerHandle{ws: ws, lastUse: time.Now()}
	m.mu.Unlock()
	// SPEC-07: open-workspace observability (paired with the Dec in
	// closeHandle — every close path routes through it).
	metrics.GaugeNamed("workspaces_open").Inc()

	// The eviction close runs OUTSIDE the mutex (F-043): a slow drain —
	// or a serve hook waiting out request refcounts — must never stall
	// unrelated Manager operations. The victim is already detached, so
	// the open bound holds throughout the close.
	if victim != nil {
		if err := m.closeHandle(victimName, victim); err != nil {
			log.Warn("engine: workspace eviction close failed", "name", victimName, "error", err)
		}
	}
	return ws, nil
}

// cached returns the shared handle for name, refreshing recency.
func (m *Manager) cached(name string) *Workspace {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if h, ok := m.handles[name]; ok {
		h.lastUse = time.Now()
		return h.ws
	}
	return nil
}

// Touch refreshes the recency of an open workspace WITHOUT returning the
// handle. The serve hot path serves many requests off one open (it does
// not re-call Workspace per request), so without a per-request recency
// touch an idle-close window evicts a workspace that is actively serving
// (SPEC-06 review fix). No-op for a closed manager or an unknown name.
func (m *Manager) Touch(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if h, ok := m.handles[name]; ok {
		h.lastUse = time.Now()
	}
}

// registryDir verifies name is a valid workspace under root and returns
// its directory: a subdirectory (symlinks resolved) containing config.yaml
// and a loadable .manifest.json, contained under root.
func (m *Manager) registryDir(name string) (string, error) {
	dir := filepath.Join(m.root, name)
	info, err := os.Stat(dir) // Stat follows symlinks: alias dirs register
	if err != nil || !info.IsDir() {
		return "", ErrUnknownWorkspace
	}
	ok, err := pathsafe.Contained(m.root, dir)
	if err != nil || !ok {
		// A symlink escaping root is an invalid registry entry — refuse.
		return "", ErrUnknownWorkspace
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		return "", ErrUnknownWorkspace
	}
	if _, err := manifest.Load(filepath.Join(dir, ".manifest.json")); err != nil {
		return "", ErrUnknownWorkspace
	}
	return dir, nil
}

// evictCandidateLocked picks the handle to evict so that opening need
// more stays within MaxOpen — WITHOUT closing it (the close runs outside
// the mutex, F-043). Caller holds m.mu. Returns ("", nil) when no
// eviction is needed.
func (m *Manager) evictCandidateLocked(need int) (string, *managerHandle) {
	if m.opts.maxOpen <= 0 || len(m.handles)+need <= m.opts.maxOpen {
		return "", nil
	}
	return m.lruLocked()
}

// lruLocked returns the least-recently-used handle, breaking ties by name
// (deterministic — map iteration order is not, F-044). Caller holds m.mu.
func (m *Manager) lruLocked() (string, *managerHandle) {
	var oldestName string
	var oldest *managerHandle
	for name, h := range m.handles {
		if oldest == nil ||
			h.lastUse.Before(oldest.lastUse) ||
			(h.lastUse.Equal(oldest.lastUse) && name < oldestName) {
			oldestName, oldest = name, h
		}
	}
	return oldestName, oldest
}

// List scans the registry and reports every valid workspace under root.
func (m *Manager) List(ctx context.Context) ([]WorkspaceInfo, error) {
	ctx = orBackground(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	m.mu.Unlock()

	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, fmt.Errorf("engine: scan manager root: %w", err)
	}
	var out []WorkspaceInfo
	for _, e := range entries {
		name := e.Name()
		if ValidateWorkspaceName(name) != nil {
			continue
		}
		dir, err := m.registryDir(name)
		if err != nil {
			continue // invalid registry entries are skipped, not errors
		}
		mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
		if err != nil {
			continue // raced a delete between registryDir and here
		}
		m.mu.Lock()
		_, open := m.handles[name]
		m.mu.Unlock()
		out = append(out, WorkspaceInfo{
			Name:            name,
			Dir:             dir,
			Open:            open,
			RequiresUpgrade: mf.IsPreFormat(),
		})
	}
	return out, nil
}

// idleLoop closes handles idle longer than d until ctx cancels or Close
// stops it. Tick is min(d/2, 30s) so idle detection lag stays bounded.
func (m *Manager) idleLoop(ctx context.Context, d time.Duration) {
	defer close(m.idleDone)
	tick := d / 2
	if tick > 30*time.Second {
		tick = 30 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.idleStop:
			return
		case now := <-t.C:
			m.evaluateIdle(now, d)
		}
	}
}

// evaluateIdle detaches every handle idle longer than d at now and closes
// the detached handles. Detach happens under the lock, close outside it
// (F-043): the close drains in-flight readers and must not stall the
// Manager. Synchronous so tests drive it with explicit timestamps instead
// of waiting on the background ticker.
func (m *Manager) evaluateIdle(now time.Time, d time.Duration) {
	var evicted []struct {
		name string
		h    *managerHandle
	}
	m.mu.Lock()
	for name, h := range m.handles {
		if now.Sub(h.lastUse) > d {
			delete(m.handles, name)
			evicted = append(evicted, struct {
				name string
				h    *managerHandle
			}{name, h})
		}
	}
	m.mu.Unlock()
	for _, e := range evicted {
		if err := m.closeHandle(e.name, e.h); err != nil {
			log.Warn("engine: idle close failed", "name", e.name, "error", err)
		}
	}
}

// Close stops the idle goroutine and closes every open handle. Idempotent.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	handles := m.handles
	m.handles = map[string]*managerHandle{}
	stop := m.idleStop
	m.mu.Unlock()

	if stop != nil {
		close(stop)
		<-m.idleDone
	}
	var firstErr error
	for name, h := range handles {
		if err := m.closeHandle(name, h); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// closeHandle closes one handle outside the mutex (Close path).
func (m *Manager) closeHandle(name string, h *managerHandle) error {
	metrics.GaugeNamed("workspaces_open").Dec()
	if m.opts.onEvict != nil {
		return m.opts.onEvict(name, h.ws)
	}
	return h.ws.Close()
}
