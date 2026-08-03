// Package app provides the single construction point for a sage-wiki
// project's shared dependencies (REL-07, P1-8): config, database, and the
// store stack every entry point used to wire by hand (web, MCP, query,
// TUI, compiler-adjacent paths). Adoption is CONSTRUCTION-ONLY — there is
// no global or singleton App; callers own their instance's lifecycle.
package app

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
)

// App bundles a project's shared dependencies.
type App struct {
	Config   *config.Config
	Backend  store.Backend
	DB       store.DBHandle // the Backend itself (ReadDB/WriteDB/WriteTx)
	Mem      store.EntryStore
	Vec      store.VectorStore
	Ont      store.OntologyStore
	Searcher *hybrid.Searcher

	embedderOnce sync.Once
	embedder     embed.Embedder
}

// Open loads config.yaml and .sage/wiki.db and wires the store stack with
// the exact merge semantics every entry point used to duplicate
// (MergedRelations/MergedEntityTypes + ValidRelationNames/
// ValidEntityTypeNames). Step order is pinned: config.Load → storage.Open
// → infallible wiring, so no fallible step follows the DB open and there
// is no partial-failure handle leak.
//
// The embedder is deliberately NOT built here (spec D1): embed.NewFromConfig
// probes Ollama over HTTP and warns in offline setups, and several entry
// points never construct one at startup — an eager embedder would be a
// behavior change disguised as a refactor. Call Embedder() at the point
// the current code builds it.
func Open(projectDir string) (*App, error) {
	return OpenWithOptions(projectDir, filepath.Join(projectDir, "config.yaml"), store.ModeWriter)
}

// ConfigLoadError marks a config.Load failure at Open, so callers
// (pkg/engine) can map exactly that failure onto their own sentinel
// without string matching.
type ConfigLoadError struct{ Err error }

// Error implements error.
func (e *ConfigLoadError) Error() string { return "load config: " + e.Err.Error() }

// Unwrap exposes the underlying config error.
func (e *ConfigLoadError) Unwrap() error { return e.Err }

// OpenWithOptions is Open parameterized by config path and open mode
// (pkg/engine needs both: WithConfigFile support, and ModeReader for
// read-only workspaces). Mode semantics are store.OpenOptions': ModeReader
// runs no migrations and fails writes with store.ErrReadOnly.
func OpenWithOptions(projectDir, configPath string, mode store.Mode) (*App, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, &ConfigLoadError{Err: err}
	}

	// Routed through the storedial seam (P2-1 T5); under backend=sqlite the
	// open is byte-identical to storage.Open of .sage/wiki.db. The Backend is
	// unwrapped to the concrete *storage.DB because App's fields are the
	// concrete store types — rewiring those is D3-move scope (plan T9).
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	backend, err := storedial.Open(cfg.Storage, store.OpenOptions{
		Mode:             mode,
		ProjectDir:       projectDir,
		ValidRelations:   ontology.ValidRelationNames(mergedRels),
		ValidEntityTypes: ontology.ValidEntityTypeNames(mergedTypes),
		ANN:              cfg.Search.ANNEnabled(),
		TemporalEnabled:  cfg.Ontology.Temporal.Enabled,
		VectorBackend:    cfg.VectorBackend(),
		Now:              config.NowUTC, // SPEC-04 D4 artifact clock
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return &App{
		Config:   cfg,
		Backend:  backend,
		DB:       backend,
		Mem:      backend.Entries(),
		Vec:      backend.Vectors(),
		Ont:      backend.Ontology(),
		Searcher: hybrid.NewSearcher(backend.Entries(), backend.Vectors()),
	}, nil
}

// Embedder returns the project's embedder, building it lazily on first
// call (sync.Once) and caching. Nil when no provider is configured —
// embed.NewFromConfig's own semantics, not an error.
func (a *App) Embedder() embed.Embedder {
	a.embedderOnce.Do(func() {
		a.embedder = embed.NewFromConfig(a.Config)
	})
	return a.embedder
}

// Close closes the backend. Idempotent (storage.DB.Close is closeOnce).
func (a *App) Close() error {
	return a.Backend.Close()
}
