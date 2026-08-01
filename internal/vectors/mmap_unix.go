//go:build unix

package vectors

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// mmapIsReal reports whether mapFile uses a true mmap on this platform
// (false on the read-file fallback platforms — the resident-memory win is
// unix-only this cycle, surfaced via a one-time warn by the caller).
const mmapIsReal = true

// mapFile maps path read-only (MAP_PRIVATE) and returns the bytes plus an
// unmap function. Pages load on demand — the matrix never enters the Go
// heap.
func mapFile(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, nil, fmt.Errorf("vectors.mapFile: %s is empty", path)
	}
	b, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("vectors.mapFile: mmap %s: %w", path, err)
	}
	return b, func() error { return unix.Munmap(b) }, nil
}
