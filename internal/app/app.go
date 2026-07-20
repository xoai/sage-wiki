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
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// App bundles a project's shared dependencies.
type App struct {
	Config   *config.Config
	DB       *storage.DB
	Mem      *memory.Store
	Vec      *vectors.Store
	Ont      *ontology.Store
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
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	mem := memory.NewStore(db)
	vec := vectors.NewStore(db)
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ont := ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes))
	searcher := hybrid.NewSearcher(mem, vec)

	return &App{
		Config:   cfg,
		DB:       db,
		Mem:      mem,
		Vec:      vec,
		Ont:      ont,
		Searcher: searcher,
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

// Close closes the database. Idempotent (storage.DB.Close is closeOnce).
func (a *App) Close() error {
	return a.DB.Close()
}
