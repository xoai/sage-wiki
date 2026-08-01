package mirror

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrShipLocked reports the ship-mutex is held by another shipper after the
// acquire timeout expired (spec.md §Single-leader: exactly one process seals
// WAL segments, rotates generations, or commits mirror-state at a time).
var ErrShipLocked = errors.New("mirror: ship-mutex held by another shipper")

// ShipMutex is the acquired single-leader mutex (.sage/mirror-ship.lock).
// flock where supported (auto-released on process death, so kill -9 cannot
// strand it), lockfile fallback with stale takeover elsewhere.
type ShipMutex struct {
	path       string
	held       *os.File
	isFallback bool
	token      string // fallback only: fences release after a stale takeover
}

// shipMutexStaleAfter mirrors the engine lockfile fallback's conservative
// window (ship cycles are short, but a live ship under a pathologically
// slow bucket must never look stale).
const shipMutexStaleAfter = 24 * time.Hour

const shipMutexRetryInterval = 25 * time.Millisecond

func shipMutexPath(dir string) string {
	return filepath.Join(dir, ".sage", "mirror-ship.lock")
}

// AcquireShipMutex blocks up to timeout for the single-leader ship-mutex.
// timeout <= 0 means a single non-blocking attempt.
func AcquireShipMutex(dir string, timeout time.Duration) (*ShipMutex, error) {
	deadline := time.Now().Add(timeout)
	for {
		m, err := acquireShipMutexOnce(dir)
		if err == nil {
			return m, nil
		}
		if !errors.Is(err, ErrShipLocked) {
			return nil, err
		}
		if time.Now().Add(shipMutexRetryInterval).After(deadline) {
			return nil, ErrShipLocked
		}
		time.Sleep(shipMutexRetryInterval)
	}
}

// Release frees the mutex. Idempotent.
func (m *ShipMutex) Release() error {
	if m == nil || m.held == nil {
		return nil
	}
	if m.isFallback {
		err := m.releaseLockfile()
		m.held = nil
		return err
	}
	err := releaseShipPlatformLock(m.held)
	m.held = nil
	return err
}

// acquireShipLockfile is the portable fallback: create-exclusive lockfile
// with a pid token, stale takeover after shipMutexStaleAfter.
func acquireShipLockfile(dir string) (*ShipMutex, error) {
	path := shipMutexPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mirror: create lock dir: %w", err)
	}
	token := fmt.Sprintf("%d %s", os.Getpid(), time.Now().UTC().Format(time.RFC3339))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		if _, werr := f.WriteString(token); werr != nil {
			f.Close()
			os.Remove(path)
			return nil, fmt.Errorf("mirror: write lockfile: %w", werr)
		}
		return &ShipMutex{path: path, held: f, isFallback: true, token: token}, nil
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("mirror: acquire lock %s: %w", path, err)
	}

	info, statErr := os.Stat(path)
	if statErr != nil || time.Since(info.ModTime()) <= shipMutexStaleAfter {
		return nil, ErrShipLocked
	}
	// Stale takeover (same accepted residual as the engine fallback: not
	// race-free between two concurrent recoverers).
	wf, werr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
	if werr != nil {
		return nil, fmt.Errorf("mirror: lock takeover %s: %w", path, werr)
	}
	if _, werr := wf.WriteString(token); werr != nil {
		wf.Close()
		return nil, fmt.Errorf("mirror: write lockfile: %w", werr)
	}
	return &ShipMutex{path: path, held: wf, isFallback: true, token: token}, nil
}

// releaseLockfile removes the lockfile, token-fenced so a stale takeover
// victim's release cannot delete the new holder's lock.
func (m *ShipMutex) releaseLockfile() error {
	if m.held != nil {
		m.held.Close()
		m.held = nil
	}
	if data, err := os.ReadFile(m.path); err == nil && string(data) != m.token {
		return nil // taken over by someone else — not ours to remove
	}
	return os.Remove(m.path)
}
