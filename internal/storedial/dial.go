// Package storedial is the storage backend factory (P2-1): the only package
// that imports all backend implementations. Consumers use Open (explicit
// config) or OpenProject (loads the project's config.yaml).
package storedial

import (
	"fmt"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/sqlitestore"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/storage/postgres"
	"github.com/xoai/sage-wiki/internal/store"
)

// Open dispatches on cfg.Backend. sqlite → sqlitestore (identical behavior to
// today's direct storage.Open). postgres → internal/storage/postgres;
// validation of DSN/dimension happens in config.Load.
func Open(cfg config.StorageConfig, opts store.OpenOptions) (store.Backend, error) {
	backend := cfg.Backend
	if backend == "" {
		backend = "sqlite"
	}
	switch backend {
	case "sqlite":
		path := filepath.Join(opts.ProjectDir, ".sage", "wiki.db")
		return sqlitestore.OpenPath(path, opts.Mode, sqlitestore.Options{
			ValidRelations:   opts.ValidRelations,
			ValidEntityTypes: opts.ValidEntityTypes,
			ANN:              opts.ANN,
			TemporalEnabled:  opts.TemporalEnabled, // P3-6: nil = enabled
			VectorBackend:    opts.VectorBackend,
		})
	case "postgres":
		return postgres.Open(cfg.DSN, opts)
	default:
		return nil, fmt.Errorf("storage: unknown storage backend %q (valid: sqlite, postgres)", backend)
	}
}

// OpenConcrete opens the project's vault DB via backend selection and returns
// the concrete *storage.DB — TRANSITIONAL (plan T6): for direct-open sites
// that still construct concrete stores; they unwrap and close the DB as
// before (backend.Close and db.Close share the underlying handle). A
// zero-value cfg selects the sqlite default (skip-list sites without config
// in scope — see decisions.md 2026-07-21); postgres returns the
// not-available error until the backend lands (T12).
func OpenConcrete(projectDir string, cfg config.StorageConfig) (*storage.DB, error) {
	backend, err := Open(cfg, store.OpenOptions{Mode: store.ModeWriter, ProjectDir: projectDir})
	if err != nil {
		return nil, err
	}
	db := sqlitestore.Unwrap(backend)
	if db == nil {
		backend.Close()
		return nil, fmt.Errorf("storage: unexpected backend type")
	}
	return db, nil
}

// OpenProject loads <projectDir>/config.yaml and opens its storage backend.
// Under backend=sqlite (default) the open is byte-identical to storage.Open
// of <projectDir>/.sage/wiki.db — same file, WAL, pragmas, read pool,
// migrations.
func OpenProject(projectDir string, mode store.Mode) (store.Backend, error) {
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return OpenWithConfig(cfg, projectDir, mode)
}

// OpenWithConfig opens the backend for an already-loaded config, computing
// OpenOptions exactly the way OpenProject does. It exists so callers that
// load config through a different path (reconcileStartup honors --config via
// resolveConfigPath) share ONE option literal — a drift in the literal
// breaks every consumer at once instead of one silently (Gate-8).
func OpenWithConfig(cfg *config.Config, projectDir string, mode store.Mode) (store.Backend, error) {
	lt, err := cfg.Storage.LockTimeoutDuration()
	if err != nil {
		return nil, err
	}
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	return Open(cfg.Storage, store.OpenOptions{
		Mode:             mode,
		ProjectDir:       projectDir,
		LockTimeout:      lt,
		Pool:             store.PoolConfig{MaxOpen: cfg.Storage.Pool.MaxOpen, MaxIdle: cfg.Storage.Pool.MaxIdle},
		VectorDimension:  cfg.Storage.VectorDimension,
		ValidRelations:   ontology.ValidRelationNames(mergedRels),
		ValidEntityTypes: ontology.ValidEntityTypeNames(mergedTypes),
		TemporalEnabled:  cfg.Ontology.Temporal.Enabled,
	})
}
