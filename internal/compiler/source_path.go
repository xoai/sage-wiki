package compiler

import (
	"path/filepath"
)

// resolveSourcePath returns the physical path for a manifest/config source.
// Relative source IDs belong to the Sage project; absolute IDs are external
// read roots and must never be prefixed with projectDir.
func resolveSourcePath(projectDir, sourcePath string) string {
	if filepath.IsAbs(sourcePath) {
		return filepath.Clean(sourcePath)
	}
	return filepath.Join(projectDir, sourcePath)
}
