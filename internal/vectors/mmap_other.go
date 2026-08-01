//go:build !unix

package vectors

import (
	"fmt"
	"os"
)

// mmapIsReal is false on fallback platforms: mapFile reads the file into a
// private heap slice — identical query semantics, NO resident-memory win.
// The caller surfaces a one-time warn; the memory ceiling is unix-only
// this cycle.
const mmapIsReal = false

// mapFile reads path fully into memory. The returned unmap is a no-op.
func mapFile(path string) ([]byte, func() error, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if len(b) == 0 {
		return nil, nil, fmt.Errorf("vectors.mapFile: %s is empty", path)
	}
	return b, func() error { return nil }, nil
}
