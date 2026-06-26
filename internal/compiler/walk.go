package compiler

import (
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
)

// WalkSourceDir walks a directory tree, resolving symlinks before walking
// so that filepath.WalkDir sees the real directory contents rather than the
// symlink node.  Go's filepath.WalkDir uses os.Lstat internally which does
// not follow symlinks — a symlink-to-directory is reported as IsDir()==false
// and no children are walked.  Only symlinks at the tree root are resolved;
// nested symlinked subdirectories are not followed (this prevents loops and
// keeps behavior predictable).
//
// For each regular file, fn is called with:
//   absPath — the resolved absolute file path (use for file I/O)
//   relPath — the path relative to the resolved directory root
func WalkSourceDir(srcDir string, fn func(absPath, relPath string, d os.DirEntry) error) error {
	walkPath := srcDir
	if resolved, err := filepath.EvalSymlinks(srcDir); err == nil {
		walkPath = resolved
	} else {
		log.Warn("walkSourceDir: failed to resolve symlinks, walking as-is",
			"path", srcDir, "error", err)
	}

	return filepath.WalkDir(walkPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		relPath, relErr := filepath.Rel(walkPath, path)
		if relErr != nil {
			log.Warn("walkSourceDir: failed to compute relative path, falling back to base name",
				"walkPath", walkPath, "path", path, "error", relErr)
			relPath = filepath.Base(path)
		}
		return fn(path, relPath, d)
	})
}
