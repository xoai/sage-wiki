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
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// StatusInfo holds wiki stats for display.
type StatusInfo struct {
	Project       string `json:"project"`
	Mode          string `json:"mode"` // greenfield or vault-overlay
	SourceCount   int    `json:"source_count"`
	PendingCount  int    `json:"pending_count"`
	ConceptCount  int    `json:"concept_count"`
	EntryCount    int    `json:"entry_count"`
	VectorCount   int    `json:"vector_count"`
	VectorDims    int    `json:"vector_dims"`
	EntityCount   int    `json:"entity_count"`
	RelationCount int    `json:"relation_count"`
	LearningCount int    `json:"learning_count"`
	EmbedProvider string `json:"embed_provider"`
	EmbedDims     int    `json:"embed_dims"`
	DimMismatch   bool   `json:"dim_mismatch"`
	GitClean      bool   `json:"git_clean"`
	LastCommit    string `json:"last_commit"`
	LastMessage   string `json:"last_message"`

	// Tier distribution (from compile_items)
	TierDistribution map[int]int    `json:"tier_distribution,omitempty"` // tier -> count
	FullyCompiled    int            `json:"fully_compiled,omitempty"`
	WithErrors       int            `json:"with_errors,omitempty"`
	AvgQuality       float64        `json:"avg_quality,omitempty"`
	SourceTypes      map[string]int `json:"source_types,omitempty"` // source_type -> count

	// Compile queue (P2-3): status -> count, present when the queue has rows.
	QueueByStatus map[string]int `json:"compile_queue,omitempty"`
}

// Stores holds shared store references to avoid re-opening the DB.
type Stores struct {
	Mem store.EntryStore
	Vec store.VectorStore
	Ont store.OntologyStore
	DB  store.DBHandle // optional — used for compile_items tier stats
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
	var memStore store.EntryStore
	var vecStore store.VectorStore
	var ontStore store.OntologyStore

	if stores != nil {
		memStore = stores.Mem
		vecStore = stores.Vec
		ontStore = stores.Ont
	} else {
		// P2-1 skip-list: wiki is imported (incl. in tests) by compiler and
		// linter, which sqlitestore imports — routing via storedial closes an
		// import cycle. Backend selection deferred to T9a caller-injection.
		dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
		db, err := storage.Open(dbPath)
		if err != nil {
			return nil, fmt.Errorf("status: open db: %w", err)
		}
		defer db.Close()

		memStore = memory.NewStore(db)
		vecStore = vectors.NewStore(db)
		mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		ontStore = ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()))

		// Provide DB for tier stats
		stores = &Stores{Mem: memStore, Vec: vecStore, Ont: ontStore, DB: db}
	}

	info.EntryCount, _ = memStore.Count()
	info.VectorCount, _ = vecStore.Count()
	info.VectorDims, _ = vecStore.Dimensions()
	info.EntityCount, _ = ontStore.EntityCount("")
	info.RelationCount, _ = ontStore.RelationCount()

	// Tier distribution from compile_items (if table exists)
	info.TierDistribution, info.FullyCompiled, info.WithErrors, info.AvgQuality, info.SourceTypes, info.QueueByStatus = queryTierStats(stores, projectDir)

	// Embedding provider
	embedder := embed.NewFromConfig(cfg)
	if embedder != nil {
		info.EmbedProvider = embedder.Name()
		info.EmbedDims = embedder.Dimensions()
		// Check dimension mismatch (0 means unknown — skip check)
		if info.VectorDims > 0 && info.EmbedDims > 0 && info.VectorDims != info.EmbedDims {
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

	if len(s.QueueByStatus) > 0 {
		out += fmt.Sprintf("Compile queue: %d pending, %d leased, %d done, %d failed\n",
			s.QueueByStatus["pending"], s.QueueByStatus["leased"], s.QueueByStatus["done"], s.QueueByStatus["failed"])
	}

	if s.LastCommit != "" {
		gitStatus := "clean"
		if !s.GitClean {
			gitStatus = "dirty"
		}
		out += fmt.Sprintf("Git: %s %s (%s)\n", s.LastCommit, s.LastMessage, gitStatus)
	}

	// Tier distribution
	if len(s.TierDistribution) > 0 {
		out += "Tiers:"
		tierNames := map[int]string{0: "index", 1: "embed", 2: "parse", 3: "compile"}
		for tier := 0; tier <= 3; tier++ {
			if count, ok := s.TierDistribution[tier]; ok && count > 0 {
				out += fmt.Sprintf(" T%d(%s)=%d", tier, tierNames[tier], count)
			}
		}
		out += "\n"
		if s.FullyCompiled > 0 || s.WithErrors > 0 {
			out += fmt.Sprintf("Compiled: %d fully", s.FullyCompiled)
			if s.WithErrors > 0 {
				out += fmt.Sprintf(", %d with errors", s.WithErrors)
			}
			if s.AvgQuality > 0 {
				out += fmt.Sprintf(", avg quality %.2f", s.AvgQuality)
			}
			out += "\n"
		}
	}

	return out
}

// queryTierStats reads compile_items table for tier distribution stats.
// Returns zero values if the table doesn't exist yet.
func queryTierStats(stores *Stores, projectDir string) (tierDist map[int]int, fullyCompiled, withErrors int, avgQuality float64, sourceTypes map[string]int, queueByStatus map[string]int) {
	tierDist = make(map[int]int)
	sourceTypes = make(map[string]int)
	queueByStatus = make(map[string]int)

	if stores == nil || stores.DB == nil {
		return
	}
	db := stores.DB

	// Check if compile_items table exists
	var tableName string
	err := db.ReadDB().QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='compile_items'").Scan(&tableName)
	if err != nil {
		return // table doesn't exist yet
	}

	// Tier distribution
	rows, err := db.ReadDB().Query("SELECT tier, COUNT(*) FROM compile_items GROUP BY tier")
	if err != nil {
		return
	}
	for rows.Next() {
		var tier, count int
		if rows.Scan(&tier, &count) == nil {
			tierDist[tier] = count
		}
	}
	rows.Close()

	// Fully compiled
	db.ReadDB().QueryRow("SELECT COUNT(*) FROM compile_items WHERE pass_written = 1").Scan(&fullyCompiled)

	// With errors
	db.ReadDB().QueryRow("SELECT COUNT(*) FROM compile_items WHERE error IS NOT NULL AND error != ''").Scan(&withErrors)

	// Avg quality
	var avgQ *float64
	if err := db.ReadDB().QueryRow("SELECT AVG(quality_score) FROM compile_items WHERE quality_score IS NOT NULL").Scan(&avgQ); err == nil && avgQ != nil {
		avgQuality = *avgQ
	}

	// Source type distribution
	rows, err = db.ReadDB().Query("SELECT source_type, COUNT(*) FROM compile_items GROUP BY source_type")
	if err != nil {
		return
	}
	defer func() {
		// Queue status counts (P2-3) — best-effort like the rest of this probe.
		srows, err := db.ReadDB().Query("SELECT status, COUNT(*) FROM compile_items GROUP BY status")
		if err != nil {
			return
		}
		defer srows.Close()
		for srows.Next() {
			var st string
			var count int
			if srows.Scan(&st, &count) == nil {
				queueByStatus[st] = count
			}
		}
	}()
	for rows.Next() {
		var st string
		var count int
		if rows.Scan(&st, &count) == nil {
			sourceTypes[st] = count
		}
	}
	rows.Close()

	return
}
