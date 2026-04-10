package wiki

import (
	"fmt"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// StatusInfo holds wiki stats for display.
type StatusInfo struct {
	Project        string
	Mode           string // greenfield or vault-overlay
	SourceCount    int
	PendingCount   int
	ConceptCount   int
	EntryCount     int
	VectorCount    int
	VectorDims     int
	EntityCount    int
	RelationCount  int
	LearningCount  int
	EmbedProvider  string
	EmbedDims      int
	DimMismatch    bool
	GitClean       bool
	LastCommit     string
	LastMessage    string
}

// Stores holds shared store references to avoid re-opening the DB.
type Stores struct {
	Mem *memory.Store
	Vec *vectors.Store
	Ont *ontology.Store
}

// GetStatus collects wiki stats from the project.
// If stores is non-nil, uses the provided stores (avoids double DB open).
// If stores is nil, opens the DB internally.
func GetStatus(projectDir string, stores *Stores) (*StatusInfo, error) {
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("status: load config: %w", err)
	}

	info := &StatusInfo{
		Project: cfg.Project,
	}

	if cfg.IsVaultOverlay() {
		info.Mode = "vault-overlay"
	} else {
		info.Mode = "greenfield"
	}

	// Load manifest
	mfPath := filepath.Join(projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return nil, fmt.Errorf("status: load manifest: %w", err)
	}
	info.SourceCount = mf.SourceCount()
	info.ConceptCount = mf.ConceptCount()
	info.PendingCount = len(mf.PendingSources())

	// Use provided stores or open DB
	var memStore *memory.Store
	var vecStore *vectors.Store
	var ontStore *ontology.Store

	if stores != nil {
		memStore = stores.Mem
		vecStore = stores.Vec
		ontStore = stores.Ont
	} else {
		dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
		db, err := storage.Open(dbPath)
		if err != nil {
			return nil, fmt.Errorf("status: open db: %w", err)
		}
		defer db.Close()

		memStore = memory.NewStore(db)
		vecStore = vectors.NewStore(db)
		ontStore = ontology.NewStore(db, ontology.ValidRelationNames(ontology.MergedRelations(cfg.Ontology.Relations)))
	}

	info.EntryCount, _ = memStore.Count()
	info.VectorCount, _ = vecStore.Count()
	info.VectorDims, _ = vecStore.Dimensions()
	info.EntityCount, _ = ontStore.EntityCount("")
	info.RelationCount, _ = ontStore.RelationCount()

	// Embedding provider
	embedder := embed.NewFromConfig(cfg)
	if embedder != nil {
		info.EmbedProvider = embedder.Name()
		info.EmbedDims = embedder.Dimensions()
		// Check dimension mismatch
		if info.VectorDims > 0 && info.VectorDims != embedder.Dimensions() {
			info.DimMismatch = true
		}
	} else {
		info.EmbedProvider = "none (BM25-only)"
	}

	// Git
	if gitpkg.IsRepo(projectDir) {
		status, _ := gitpkg.Status(projectDir)
		info.GitClean = status == ""
		hash, msg, _ := gitpkg.LastCommit(projectDir)
		info.LastCommit = hash
		info.LastMessage = msg
	}

	return info, nil
}

// FormatStatus renders StatusInfo as a human-readable string.
func FormatStatus(s *StatusInfo) string {
	out := fmt.Sprintf("Project: %s (%s)\n", s.Project, s.Mode)
	out += fmt.Sprintf("Sources: %d (%d pending)\n", s.SourceCount, s.PendingCount)
	out += fmt.Sprintf("Concepts: %d\n", s.ConceptCount)
	out += fmt.Sprintf("Entries: %d indexed\n", s.EntryCount)
	out += fmt.Sprintf("Vectors: %d", s.VectorCount)
	if s.VectorDims > 0 {
		out += fmt.Sprintf(" (%d-dim)", s.VectorDims)
	}
	out += "\n"
	out += fmt.Sprintf("Entities: %d, Relations: %d\n", s.EntityCount, s.RelationCount)
	out += fmt.Sprintf("Embedding: %s", s.EmbedProvider)
	if s.EmbedDims > 0 {
		out += fmt.Sprintf(" (%d-dim)", s.EmbedDims)
	}
	out += "\n"
	if s.DimMismatch {
		out += fmt.Sprintf("  WARNING: dimension mismatch (stored: %d-dim, provider: %d-dim) — re-embed on next compile\n", s.VectorDims, s.EmbedDims)
	}

	if s.LastCommit != "" {
		gitStatus := "clean"
		if !s.GitClean {
			gitStatus = "dirty"
		}
		out += fmt.Sprintf("Git: %s %s (%s)\n", s.LastCommit, s.LastMessage, gitStatus)
	}

	return out
}
