package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// CurrentFormatVersion is the workspace format written today. The field is
// NEW (SPEC-01): the pre-existing `version` field has been 2 since the
// initial commit, so it cannot discriminate v0.2.x workspaces — an ABSENT
// format_version is what marks them.
const CurrentFormatVersion = 1

// EngineVersion is stamped into new manifests (set by the CLI at startup,
// same pattern as mcp.Version); "dev" otherwise.
var EngineVersion = "dev"

// Manifest tracks sources, concepts, and their relationships.
type Manifest struct {
	Version    int                `json:"version"`
	Sources    map[string]Source  `json:"sources"`
	Concepts   map[string]Concept `json:"concepts"`
	EmbedModel string             `json:"embed_model,omitempty"`
	EmbedDim   int                `json:"embed_dim,omitempty"`

	// FormatVersion is the workspace format discriminator (SPEC-01). Zero
	// (absent on disk) = v0.2.x workspace: opens read-only until adopted
	// via an explicit upgrade consent.
	FormatVersion int `json:"format_version,omitempty"`
	// Engine is the engine version that last wrote the manifest.
	Engine string `json:"engine_version,omitempty"`
	// CreatedAt is the workspace creation time (RFC3339), set at init.
	CreatedAt string `json:"created_at,omitempty"`

	// now is the injectable clock (SPEC-04 D4): timestamps stamp from it so a
	// pinned SOURCE_DATE_EPOCH propagates into the manifest. Never serialized.
	now func() time.Time
}

// SetNow installs the clock used for every subsequent timestamp this
// manifest stamps (AddSource, MarkCompiled, AddConcept). The default is
// time.Now; compile paths inject the SDE-aware config.NowUTC.
func (m *Manifest) SetNow(now func() time.Time) {
	m.now = now
}

func (m *Manifest) nowUTC() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

// IsPreFormat reports whether the manifest predates format versioning —
// a v0.2.x workspace in SPEC-01 terms.
func (m *Manifest) IsPreFormat() bool {
	return m.FormatVersion == 0
}

// Source represents a tracked source file.
type Source struct {
	Hash             string   `json:"hash"`
	Type             string   `json:"type"`
	SizeBytes        int64    `json:"size_bytes"`
	AddedAt          string   `json:"added_at"`
	CompiledAt       string   `json:"compiled_at,omitempty"` // Deprecated: use compile_items table
	SummaryPath      string   `json:"summary_path,omitempty"`
	ConceptsProduced []string `json:"concepts_produced,omitempty"`
	ChunkCount       int      `json:"chunk_count,omitempty"`
	Status           string   `json:"status"`                // Deprecated: use compile_items table
	Tier             int      `json:"tier,omitempty"`        // 0-3, compilation tier
	SourceType       string   `json:"source_type,omitempty"` // compiler, scribe, manual
}

// Concept represents a tracked concept.
type Concept struct {
	ArticlePath string   `json:"article_path"`
	Sources     []string `json:"sources"`
	// Aliases carries the concept's extracted alias names (issue #128).
	// omitempty + additive: old binaries ignore it on read; an old binary's
	// Load+Save silently strips it on any manifest write (documented).
	Aliases      []string `json:"aliases,omitempty"`
	LastCompiled string   `json:"last_compiled"`
}

// New creates an empty manifest stamped with the current workspace format.
func New() *Manifest {
	return NewWithClock(nil)
}

// NewWithClock is New with an injectable clock (SPEC-04 D4); nil keeps the
// wall-clock default.
func NewWithClock(now func() time.Time) *Manifest {
	m := &Manifest{
		Version:       2,
		Sources:       make(map[string]Source),
		Concepts:      make(map[string]Concept),
		FormatVersion: CurrentFormatVersion,
		Engine:        EngineVersion,
	}
	m.now = now
	m.CreatedAt = m.nowUTC().Format(time.RFC3339)
	return m
}

// Load reads a manifest from disk.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("manifest.Load: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest.Load: %w", err)
	}

	if m.Sources == nil {
		m.Sources = make(map[string]Source)
	}
	if m.Concepts == nil {
		m.Concepts = make(map[string]Concept)
	}

	return &m, nil
}

// Save writes the manifest to disk atomically: it marshals to a sibling temp
// file and renames it into place, so a crash mid-write can never leave an
// unparseable `.manifest.json` and a concurrent reader never observes a partial
// write (D2/I1). The temp lives next to the target so the rename stays on one
// filesystem (rename is only atomic within a filesystem).
func (m *Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest.Save: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("manifest.Save: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup; leaving a stale temp is not fatal
		return fmt.Errorf("manifest.Save: rename: %w", err)
	}
	return nil
}

// Mutate performs a serialized read-modify-write of the manifest at path. It
// acquires the exclusive advisory lock, loads a FRESH copy from disk (so it sees
// every other writer's committed mutation — never a stale in-memory copy),
// applies fn, saves atomically, and releases the lock. Every short-transaction
// writer (MCP tools, ingest, CLI) must go through this entry point so no writer
// clobbers another's committed mutation (I1/D4). Long-running owners (compile)
// use MergeSave instead, which merges a Load-time delta onto the fresh reload.
func Mutate(ctx context.Context, path string, fn func(*Manifest) error) error {
	return mutateWithOpts(ctx, path, defaultLockOptions(), fn)
}

// mutateWithOpts is Mutate with explicit lock timing (used by tests).
func mutateWithOpts(ctx context.Context, path string, opts lockOptions, fn func(*Manifest) error) error {
	lock, err := acquireLockOpts(ctx, path, opts)
	if err != nil {
		return fmt.Errorf("manifest.Mutate: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	m, err := Load(path)
	if err != nil {
		return fmt.Errorf("manifest.Mutate: load: %w", err)
	}
	if err := fn(m); err != nil {
		return err // caller's error, surfaced verbatim
	}
	if err := m.Save(path); err != nil {
		return fmt.Errorf("manifest.Mutate: save: %w", err)
	}
	return nil
}

// AddSource registers a new source file.
func (m *Manifest) AddSource(path string, hash string, typ string, size int64) {
	m.Sources[path] = Source{
		Hash:      hash,
		Type:      typ,
		SizeBytes: size,
		AddedAt:   m.nowUTC().Format(time.RFC3339),
		Status:    "pending",
	}
}

// MarkCompiled marks a source as compiled.
func (m *Manifest) MarkCompiled(path string, summaryPath string, concepts []string) {
	if s, ok := m.Sources[path]; ok {
		s.CompiledAt = m.nowUTC().Format(time.RFC3339)
		s.SummaryPath = summaryPath
		s.ConceptsProduced = concepts
		s.Status = "compiled"
		m.Sources[path] = s
	}
}

// RemoveSource removes a source entry.
func (m *Manifest) RemoveSource(path string) {
	delete(m.Sources, path)
}

// AddConcept registers a concept.
// AddConcept records a concept. On re-add it UNIONS sources and aliases
// (dedup, order-preserving) instead of replacing — a replace would wipe
// accumulated evidence and, once aliases exist, the very aliases acronym
// dedup relies on (issue #128).
func (m *Manifest) AddConcept(name string, articlePath string, sources []string, aliases ...string) {
	c := m.Concepts[name]
	newSources := unionStrings(c.Sources, sources)
	newAliases := unionStrings(c.Aliases, aliases)
	changed := c.ArticlePath != articlePath ||
		len(newSources) != len(c.Sources) || len(newAliases) != len(c.Aliases) ||
		!sameStrings(newSources, c.Sources) || !sameStrings(newAliases, c.Aliases)
	c.ArticlePath = articlePath
	c.Sources = newSources
	c.Aliases = newAliases
	if changed {
		// Every compile re-adds every extracted concept; bumping the
		// timestamp unconditionally dirtied the manifest for AutoCommit
		// vaults on no-change recompiles (review).
		c.LastCompiled = m.nowUTC().Format(time.RFC3339)
	}
	m.Concepts[name] = c
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unionStrings appends items not already present, preserving order.
func unionStrings(existing, add []string) []string {
	if len(add) == 0 {
		return existing
	}
	seen := make(map[string]bool, len(existing))
	for _, s := range existing {
		seen[s] = true
	}
	out := append([]string(nil), existing...)
	for _, s := range add {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PendingSources returns sources with status "pending".
func (m *Manifest) PendingSources() map[string]Source {
	pending := make(map[string]Source)
	for path, s := range m.Sources {
		if s.Status == "pending" {
			pending[path] = s
		}
	}
	return pending
}

// ArticlesFromSource returns concept names whose Sources list contains the given path.
func (m *Manifest) ArticlesFromSource(sourcePath string) []string {
	var names []string
	for name, c := range m.Concepts {
		for _, s := range c.Sources {
			if s == sourcePath {
				names = append(names, name)
				break
			}
		}
	}
	// SPEC-04 D1: sorted — drives prune/delete order and CLI output.
	sort.Strings(names)
	return names
}

// SourcesForArticle returns the source paths for a given concept name.
func (m *Manifest) SourcesForArticle(conceptName string) []string {
	if c, ok := m.Concepts[conceptName]; ok {
		return c.Sources
	}
	return nil
}

// SourceCount returns the total number of tracked sources.
func (m *Manifest) SourceCount() int {
	return len(m.Sources)
}

// ConceptCount returns the total number of tracked concepts.
func (m *Manifest) ConceptCount() int {
	return len(m.Concepts)
}
