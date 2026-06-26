package compiler

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
)

// DiffResult holds the change sets from comparing sources against the manifest.
type DiffResult struct {
	Added    []SourceInfo
	Modified []SourceInfo
	Removed  []string // paths of removed sources
}

// SourceInfo describes a source file.
type SourceInfo struct {
	Path     string
	Hash     string
	Type     string
	Size     int64
}

// Diff scans source directories and compares against the manifest.
func Diff(projectDir string, cfg *config.Config, mf *manifest.Manifest) (*DiffResult, error) {
	result := &DiffResult{}

	// Collect current source files
	current := make(map[string]SourceInfo)

	for _, src := range cfg.Sources {
		var srcDir string
		if filepath.IsAbs(src.Path) {
			srcDir = src.Path
		} else {
			srcDir = filepath.Join(projectDir, src.Path)
		}

		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			log.Warn("source directory not found", "path", srcDir)
			continue
		}

		if err := WalkSourceDir(srcDir, func(absPath, relPath string, d os.DirEntry) error {
			// Skip hidden files (.DS_Store etc.)
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}

			// Build manifest path: <source-name>/<path-within-source>
			manifestPath := filepath.ToSlash(filepath.Join(src.Path, relPath))
			// Normalize absolute source paths to project-relative keys so
			// manifest entries survive across runs (the old filepath.Rel
			// from projectDir gave e.g. ../../external/x.md, not /abs/x.md).
			if filepath.IsAbs(manifestPath) {
				if rel, err := filepath.Rel(projectDir, manifestPath); err == nil {
					manifestPath = filepath.ToSlash(rel)
				}
			}

			// Check ignore list
			if isIgnored(manifestPath, cfg.Ignore) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			hash, err := fileHash(absPath)
			if err != nil {
				log.Warn("failed to hash file", "path", manifestPath, "error", err)
				return nil
			}

			// Configured Source.Type takes precedence over extension/signal detection.
			// When config declares a type, bypass the manifest cache so config
			// changes take effect without --fresh.
			configuredType := cfg.TypeForPath(projectDir, manifestPath)

			var detectedType string
			if configuredType != "" {
				detectedType = configuredType
			} else if len(cfg.TypeSignals) > 0 {
				// Reuse cached type from manifest if file is unchanged
				if existing, ok := mf.Sources[manifestPath]; ok && existing.Hash == hash && existing.Type != "" {
					detectedType = existing.Type
				} else {
					contentHead := extract.ReadHead(absPath, extract.DefaultHeadRunes)
					detectedType = extract.DetectSourceTypeWithSignals(absPath, contentHead, convertSignals(cfg.TypeSignals))
				}
			} else {
				detectedType = extract.DetectSourceType(absPath)
			}
			current[manifestPath] = SourceInfo{
				Path: manifestPath,
				Hash: hash,
				Type: detectedType,
				Size: info.Size(),
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("diff: walk %s: %w", srcDir, err)
		}
	}

	// Compare against manifest. A file is Modified when either the content
	// (hash) changed OR the resolved source type changed — the latter covers
	// the case where the user updates cfg.Sources[].type or type_signals and
	// re-runs compile without modifying file contents.
	for path, info := range current {
		existing, exists := mf.Sources[path]
		if !exists {
			result.Added = append(result.Added, info)
		} else if existing.Hash != info.Hash || existing.Type != info.Type {
			result.Modified = append(result.Modified, info)
		}
	}

	// Find removed sources
	for path := range mf.Sources {
		if _, exists := current[path]; !exists {
			result.Removed = append(result.Removed, path)
		}
	}

	log.Info("diff complete",
		"added", len(result.Added),
		"modified", len(result.Modified),
		"removed", len(result.Removed),
	)

	return result, nil
}

// fileHash computes SHA-256 hash of a file.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}

// isIgnored checks if a path matches any ignore pattern.
// Supports: folder names anywhere in path (e.g. "assets"), glob extensions (e.g. "*.png"),
// and prefix matching (e.g. "_wiki").
func isIgnored(relPath string, ignore []string) bool {
	for _, pattern := range ignore {
		// Glob extension pattern (e.g. "*.png")
		if strings.HasPrefix(pattern, "*.") {
			ext := pattern[1:] // ".png"
			if strings.HasSuffix(strings.ToLower(relPath), strings.ToLower(ext)) {
				return true
			}
			continue
		}
		// Prefix match (original behavior)
		if strings.HasPrefix(relPath, pattern+"/") || strings.HasPrefix(relPath, pattern+"\\") {
			return true
		}
		if relPath == pattern {
			return true
		}
		// Match folder name anywhere in path (e.g. "assets" matches "raw/x/assets/y.png")
		if strings.Contains(relPath, "/"+pattern+"/") || strings.Contains(relPath, "\\"+pattern+"\\") {
			return true
		}
		// Also match trailing segment (e.g. path ends with "/assets")
		if strings.HasSuffix(relPath, "/"+pattern) || strings.HasSuffix(relPath, "\\"+pattern) {
			return true
		}
	}
	return false
}
