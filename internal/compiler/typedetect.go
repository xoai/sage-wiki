package compiler

import (
	"github.com/xoai/sage-wiki/internal/config"
)

// TypeForFile resolves the source type for a file path.
// It checks the configured sources first (via cfg.TypeForPath); if no source
// declares an explicit type for the path's root, it falls back to extension
// and type-signal detection.
//
// Both unset and "auto" source types fall through to detection — only an
// explicit, non-auto Source.Type overrides per-file detection.
func TypeForFile(projectDir, path string, cfg *config.Config) string {
	if t := cfg.TypeForPath(projectDir, path); t != "" {
		return t
	}
	return extractType(path, cfg.TypeSignals)
}
