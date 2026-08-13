// Package scribe provides an interface for ingesting knowledge from
// non-document sources (conversations, git commits, issue trackers)
// into the wiki's ontology layer.
//
// The scribe pattern generalizes the "extract entities from unstructured
// input" workflow. Each scribe implementation targets a specific input
// format (session JSONL, git log, GitHub issues) and produces entities
// and relations that integrate with the existing wiki ontology.
package scribe

import (
	"context"

	"github.com/xoai/sage-wiki/internal/ontology"
)

// Scribe processes raw input and produces entities and relations for the wiki.
type Scribe interface {
	// Name returns the scribe identifier (e.g., "session", "git-commit").
	Name() string

	// Process takes raw input and returns entities and relations to add.
	// The scribe is responsible for deduplication against existing entities
	// (search before add).
	Process(ctx context.Context, input []byte) (*Result, error)
}

// Result holds the output of a scribe processing run.
type Result struct {
	Entities  []ontology.Entity
	Relations []ontology.Relation
	Sources   []SourceFile // optional: generated source files to add to raw/

	// Metadata
	InputSize    int // original input size in bytes
	CompressedTo int // size after compression (0 if no compression)
	Extracted    int // entity candidates found
	Kept         int // entities after dedup/filtering
	Skipped      int // entities filtered out (duplicates, noise)
}

// SourceFile represents a file to be written to the source directory.
type SourceFile struct {
	Path    string // relative path within raw/ (e.g., "captures/session-20260414.md")
	Content []byte
	Type    string // source type for manifest
}
