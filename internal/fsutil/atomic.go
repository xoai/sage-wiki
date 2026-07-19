// Package fsutil provides small filesystem helpers shared across the codebase.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path atomically. It writes a uniquely-named
// sibling temp file, fsync is left to the OS, then renames the temp over path.
// A crash mid-write can never leave a partial or truncated file, and a
// concurrent reader never observes one — the destination changes only via the
// rename of a fully-written temp (I2, the write half of write-then-index).
//
// The temp is created in path's own directory (so the rename stays within one
// filesystem, where rename is atomic) with a unique, hidden name (so two writers
// to the same path never collide on the temp, and the reconciler's scan ignores
// the dotfile). On any failure the temp is removed and any pre-existing file at
// path is left untouched.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsutil.WriteFileAtomic: create temp in %s: %w", dir, err)
	}
	tmp := f.Name()

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsutil.WriteFileAtomic: write temp: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsutil.WriteFileAtomic: chmod temp: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fsutil.WriteFileAtomic: close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fsutil.WriteFileAtomic: rename: %w", err)
	}
	return nil
}
