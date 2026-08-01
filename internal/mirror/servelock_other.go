//go:build !unix

package mirror

import (
	"os"
	"path/filepath"
	"time"
)

// probeServeLock on non-unix platforms: the engine lockfile fallback leaves
// a file with a fresh mtime while held... the fallback lockfile has no
// heartbeat, so presence alone is ambiguous; report false (the restart note
// is advisory only).
func probeServeLock(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".sage", "engine.lock"))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) <= time.Minute
}
