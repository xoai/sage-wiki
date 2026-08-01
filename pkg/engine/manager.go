package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/manifest"
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
// evictions, and the idle-close goroutine never interleave on the map.
// Eviction of a handle with in-flight operations BLOCKS on the Workspace's
// RWMutex until readers drain (uncancellable — stdlib RWMutex has no
// ctx-aware acquisition); a perpetually hot workspace therefore cannot be
// evicted, so MaxOpen bounds OPEN handles, not eviction promptness.
// Callers MUST NOT retain handles across Manager calls that may evict; a
// retained evicted handle reports "workspace is closed".
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
func (m *Manager) Workspace(ctx context.Context, name string) (*Workspace, error) {
	ctx = orBackground(ctx)
	if err := ValidateWorkspaceName(name); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	if h, ok := m.handles[name]; ok {
		h.lastUse = time.Now()
		m.mu.Unlock()
		return h.ws, nil
	}
	m.mu.Unlock()

	// Registry + containment check outside the lock (filesystem work).
	dir, err := m.registryDir(name)
	if err != nil {
		return nil, err
	}
	optFns := []Option{}
	if m.opts.perWorkspaceOptions != nil {
		optFns = m.opts.perWorkspaceOptions(name)
	}
	ws, err := Open(ctx, dir, optFns...)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		_ = ws.Close()
		return nil, ErrManagerClosed
	}
	// A concurrent opener may have won the race; close our duplicate.
	if h, ok := m.handles[name]; ok {
		_ = ws.Close()
		h.lastUse = time.Now()
		return h.ws, nil
	}
	if err := m.evictLocked(1); err != nil {
		_ = ws.Close()
		return nil, err
	}
	m.handles[name] = &managerHandle{ws: ws, lastUse: time.Now()}
	return ws, nil
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

// evictLocked closes least-recently-used handles until opening need more
// is within MaxOpen. Caller holds m.mu.
func (m *Manager) evictLocked(need int) error {
	if m.opts.maxOpen <= 0 {
		return nil
	}
	for len(m.handles)+need > m.opts.maxOpen {
		name, h := m.lruLocked()
		if h == nil {
			return nil
		}
		delete(m.handles, name)
		if err := m.closeHandleLocked(name, h); err != nil {
			return err
		}
	}
	return nil
}

// lruLocked returns the least-recently-used handle. Caller holds m.mu.
func (m *Manager) lruLocked() (string, *managerHandle) {
	var oldestName string
	var oldest *managerHandle
	for name, h := range m.handles {
		if oldest == nil || h.lastUse.Before(oldest.lastUse) {
			oldestName, oldest = name, h
		}
	}
	return oldestName, oldest
}

// closeHandleLocked closes one handle via the eviction path. Caller holds
// m.mu — the hook (or Workspace.Close) may block on in-flight readers;
// see the synchronization contract on Manager.
func (m *Manager) closeHandleLocked(name string, h *managerHandle) error {
	if m.opts.onEvict != nil {
		return m.opts.onEvict(name, h.ws)
	}
	return h.ws.Close()
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
			m.mu.Lock()
			for name, h := range m.handles {
				if now.Sub(h.lastUse) > d {
					delete(m.handles, name)
					// Blocking close under the lock is the documented
					// contract (see Manager): idle close never kills a
					// handle mid-read — Workspace.Close drains first.
					_ = m.closeHandleLocked(name, h)
				}
			}
			m.mu.Unlock()
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
	if m.opts.onEvict != nil {
		return m.opts.onEvict(name, h.ws)
	}
	return h.ws.Close()
}
