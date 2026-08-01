package serve

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// workspaceStack is one workspace's full serve surface in multi-workspace
// mode (SPEC-06): the engine handle (lock), deps, MCP server, and HTTP
// handler — the same assembly single-workspace serve performs, bounded by
// the Manager's LRU. Requests hold a ref; the stack closes only at
// refcount zero.
type workspaceStack struct {
	name    string
	ws      *engine.Workspace
	deps    *Deps
	mcp     *mcppkg.Server
	srv     *Server
	handler http.Handler

	mu      sync.Mutex
	refs    int
	closing bool
	drained *sync.Cond
}

func (st *workspaceStack) acquireRef() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closing {
		return false
	}
	st.refs++
	return true
}

func (st *workspaceStack) release() {
	st.mu.Lock()
	st.refs--
	if st.refs == 0 {
		st.drained.Broadcast()
	}
	st.mu.Unlock()
}

// close marks the stack closing, waits for in-flight requests to drain
// (uncancellable — requests are short), then runs the full drain
// sequence: queue stop → MCP stream + MCP server close (its app handle —
// F-049) → deps close → workspace lock LAST (Server.Shutdown's
// documented order).
func (st *workspaceStack) close() error {
	st.mu.Lock()
	st.closing = true
	for st.refs > 0 {
		st.drained.Wait()
	}
	st.mu.Unlock()
	var firstErr error
	if err := st.srv.Shutdown(); err != nil {
		firstErr = err
	}
	if st.mcp != nil {
		if err := st.mcp.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// stackRegistry owns the live stacks and bridges Manager eviction to
// stack teardown (the WithOnEvict seam): the Manager owns WHEN eviction
// happens; the registry owns HOW — waiting out request refcounts before
// closing. One stack per workspace; the Manager's MaxOpen bounds them.
type stackRegistry struct {
	rootCtx     context.Context // queue workers live this long
	maxCompiles int
	compileSem  chan struct{} // root-shared compile gate (SPEC-06 Task 10)

	mgr    *engine.Manager // set after OpenManager (hook needs the registry first)
	mu     sync.Mutex
	stacks map[string]*workspaceStack
}

func newStackRegistry(rootCtx context.Context, maxCompiles int) *stackRegistry {
	return &stackRegistry{
		rootCtx:     rootCtx,
		maxCompiles: maxCompiles,
		stacks:      map[string]*workspaceStack{},
	}
}

// acquire returns the stack for name with a ref held (get-or-create).
// Callers MUST release.
func (r *stackRegistry) acquire(ctx context.Context, name string) (*workspaceStack, error) {
	r.mu.Lock()
	if st, ok := r.stacks[name]; ok && st.acquireRef() {
		r.mu.Unlock()
		return st, nil
	}
	r.mu.Unlock()

	st, err := r.assemble(ctx, name)
	if err != nil {
		return nil, err
	}
	// F-050: an LRU/idle eviction may have closed our engine handle
	// mid-assembly (the hook finds no registered stack and closes
	// directly). Re-fetch: if the Manager hands back a DIFFERENT handle,
	// ours was evicted — swap in the fresh one (only ws/SetWorkspace
	// reference it; deps and the MCP server hold their own app handles).
	// Without this the registry would serve a dead stack until restart.
	ws2, err := r.mgr.Workspace(ctx, name)
	if err != nil {
		st.srv.ClearWorkspace() // ws1 is already closed by the hook; never double-close
		_ = st.close()
		return nil, err
	}
	if ws2 != st.ws {
		st.ws = ws2
		st.srv.SetWorkspace(ws2)
	}
	r.mu.Lock()
	// Lost an assembly race: adopt the winner, tear our duplicate down.
	// The duplicate's engine handle is the SAME Manager-owned shared
	// handle the winner uses — detach it before teardown or the close
	// would kill the live workspace (F-034).
	if existing, ok := r.stacks[name]; ok && existing.acquireRef() {
		r.mu.Unlock()
		st.srv.ClearWorkspace()
		go func() {
			if err := st.close(); err != nil {
				log.Warn("serve: duplicate stack teardown failed", "workspace", name, "error", err)
			}
		}()
		return existing, nil
	}
	st.refs = 1
	r.stacks[name] = st
	r.mu.Unlock()
	return st, nil
}

// assemble builds one stack: engine handle via the Manager (LRU home),
// then the SPEC-02 surface over it. On failure after the Manager open,
// the engine handle stays cached in the Manager (closed on its Close or
// a later eviction) — no leak.
func (r *stackRegistry) assemble(ctx context.Context, name string) (*workspaceStack, error) {
	ws, err := r.mgr.Workspace(ctx, name)
	if err != nil {
		return nil, err
	}
	if ws.RequiresUpgrade() {
		return nil, fmt.Errorf("serve: workspace %q predates format versioning — "+
			"adopt it first (e.g. sage-wiki compile --upgrade --project %s)", name, ws.Dir())
	}
	dir := ws.Dir()
	deps, err := AssembleDeps(dir)
	if err != nil {
		return nil, err
	}
	mcpSrv, err := mcppkg.NewServer(dir, deps.Coordinator())
	if err != nil {
		deps.Close()
		return nil, err
	}
	srv, err := New(deps, mcpSrv, Config{
		Workspace:             dir,
		MaxConcurrentCompiles: r.maxCompiles,
		CompileSem:            r.compileSem,
		ReadyFn:               func() bool { return true }, // assembled = ready
	})
	if err != nil {
		deps.Close()
		return nil, err
	}
	srv.SetWorkspace(ws)
	srv.StartQueue(r.rootCtx)
	st := &workspaceStack{
		name:    name,
		ws:      ws,
		deps:    deps,
		mcp:     mcpSrv,
		srv:     srv,
		handler: srv.Handler(),
	}
	st.drained = sync.NewCond(&st.mu)
	return st, nil
}

// evict is the Manager's WithOnEvict hook: remove the stack from the
// registry, then close it (waiting out refs). Called INSTEAD of
// ws.Close — the stack's Shutdown closes the workspace itself. It runs
// OUTSIDE the Manager mutex (F-043), so the wait blocks only this
// eviction, never unrelated Manager operations; it cannot deadlock
// because refcount release is stack-local.
func (r *stackRegistry) evict(name string, ws *engine.Workspace) error {
	r.mu.Lock()
	st, ok := r.stacks[name]
	delete(r.stacks, name)
	r.mu.Unlock()
	if !ok {
		// No stack for this handle (shouldn't happen — every Manager
		// handle in serve mode is registry-created): close it directly.
		return ws.Close()
	}
	return st.close()
}

// closeAll tears down every stack (root shutdown path).
func (r *stackRegistry) closeAll() error {
	r.mu.Lock()
	stacks := r.stacks
	r.stacks = map[string]*workspaceStack{}
	r.mu.Unlock()
	var firstErr error
	for _, st := range stacks {
		if err := st.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
