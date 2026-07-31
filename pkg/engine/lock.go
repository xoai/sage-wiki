// Package engine is the supported embedding surface for sage-wiki (SPEC-01).
// It is a facade plus types: all logic lives in internal/ packages; this
// package only adapts them into a stable, workspace-scoped API.
//
// One Workspace per directory. Open takes an exclusive advisory lock
// (flock with a lockfile fallback) so two processes — or two Workspaces in
// one process — cannot write the same workspace concurrently; WithReadOnly
// opens without the lock for read-only consumers. The metrics registry and
// the slog logger remain process-global (telemetry, not behavior);
// per-workspace behavior (prompt overrides, storage, LLM provider) is
// carried on the Workspace.
package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLocked reports that another process (or Workspace) holds the
// workspace's write lock.
var ErrLocked = errors.New("engine: workspace is locked by another process")

// lockFileName is the lock's location inside the workspace.
const lockFileName = "engine.lock"

// workspaceLock is an acquired exclusive lock. Release with release().
type workspaceLock struct {
	path       string
	held       *os.File // flock path: the open, locked file
	isFallback bool     // lockfile fallback: release removes the file
	token      string   // fallback only: fences release after a stale takeover
}

func lockPath(dir string) string {
	return filepath.Join(dir, ".sage", lockFileName)
}

// acquireLock takes the exclusive workspace lock via the platform
// implementation (flock where supported, lockfile fallback elsewhere).
func acquireLock(dir string) (*workspaceLock, error) {
	return acquirePlatformLock(dir)
}

func (l *workspaceLock) release() error {
	if l == nil || l.held == nil {
		return nil
	}
	if l.isFallback {
		err := releaseLockfile(l)
		l.held = nil
		return err
	}
	err := releasePlatformLock(l.held)
	l.held = nil
	return err
}

// --- lockfile fallback (shared by lock_other.go and tests) ---

// fallbackStaleAfter is how old an untouched lockfile must be before a
// crashed holder's lock is taken over. Conservative on purpose: the
// fallback has no heartbeat, so a live long-running compile must never
// look stale. 24h covers any realistic single compile.
const fallbackStaleAfter = 24 * time.Hour

// acquireLockfile is the portable fallback: create-exclusive lockfile with
// a pid token, stale takeover after fallbackStaleAfter. Used on platforms
// without flock (lock_other.go) and exercised directly by tests.
func acquireLockfile(dir string) (*workspaceLock, error) {
	path := lockPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("engine: create lock dir: %w", err)
	}
	token := fmt.Sprintf("%d %s", os.Getpid(), time.Now().UTC().Format(time.RFC3339))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		if _, werr := f.WriteString(token); werr != nil {
			f.Close()
			os.Remove(path)
			return nil, fmt.Errorf("engine: write lockfile: %w", werr)
		}
		return &workspaceLock{path: path, held: f, isFallback: true, token: token}, nil
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("engine: acquire lock %s: %w", path, err)
	}

	// The lock exists: live holder, or a crashed holder's stale lockfile.
	info, statErr := os.Stat(path)
	if statErr != nil || time.Since(info.ModTime()) <= fallbackStaleAfter {
		return nil, ErrLocked
	}
	// Stale: take over by truncating and writing our token. Residual
	// (accepted): without an OS primitive this takeover is not race-free
	// between two concurrent recoverers; the window requires a genuine
	// crash AND two recoveries in the same instant.
	wf, werr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if werr != nil {
		return nil, fmt.Errorf("engine: lock takeover %s: %w", path, werr)
	}
	if _, werr := wf.WriteString(token); werr != nil {
		wf.Close()
		return nil, fmt.Errorf("engine: write lockfile: %w", werr)
	}
	return &workspaceLock{path: path, held: wf, isFallback: true, token: token}, nil
}

// releaseLockfile removes the lockfile and closes the handle. The token
// fences the remove: after a stale takeover handed the lock to someone
// else, the old holder's release must not delete the new holder's lock.
func releaseLockfile(l *workspaceLock) error {
	if l.held != nil {
		l.held.Close()
		l.held = nil
	}
	if data, err := os.ReadFile(l.path); err == nil && string(data) != l.token {
		return nil // not ours (taken over) — leave it
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("engine: remove lockfile: %w", err)
	}
	return nil
}
