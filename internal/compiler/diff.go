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
	sourcePaths := cfg.ResolveSources(projectDir)

	for _, srcDir := range sourcePaths {
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			log.Warn("source directory not found", "path", srcDir)
			continue
		}

		if err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}

			// 跳过隐藏文件（.DS_Store 等）
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}

			// Get relative path from project root
			relPath, _ := filepath.Rel(projectDir, path)

			// Check ignore list
			if isIgnored(relPath, cfg.Ignore) {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			hash, err := fileHash(path)
			if err != nil {
				log.Warn("failed to hash file", "path", relPath, "error", err)
				return nil
			}

			var contentHead string
			if len(cfg.TypeSignals) > 0 {
				contentHead = extract.ReadHead(path, extract.DefaultHeadRunes)
			}
			current[relPath] = SourceInfo{
				Path: relPath,
				Hash: hash,
				Type: extract.DetectSourceTypeWithSignals(path, contentHead, convertSignals(cfg.TypeSignals)),
				Size: info.Size(),
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("diff: walk %s: %w", srcDir, err)
		}
	}

	// Compare against manifest
	for path, info := range current {
		existing, exists := mf.Sources[path]
		if !exists {
			result.Added = append(result.Added, info)
		} else if existing.Hash != info.Hash {
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
