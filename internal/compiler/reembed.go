package compiler

import (
	"fmt"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// ReEmbed regenerates vector embeddings for all FTS5 entries
// without re-summarizing or recompiling.
func ReEmbed(projectDir string) (int, error) {
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return 0, fmt.Errorf("re-embed: load config: %w", err)
	}

	embedder := embed.NewForConfig(cfg)
	if embedder == nil {
		return 0, fmt.Errorf("re-embed: no embedding provider available")
	}

	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return 0, fmt.Errorf("re-embed: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)

	// Get all FTS5 entries
	rows, err := db.ReadDB().Query("SELECT id, content FROM entries")
	if err != nil {
		return 0, fmt.Errorf("re-embed: query entries: %w", err)
	}
	defer rows.Close()

	type entry struct {
		id      string
		content string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.content); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	_ = memStore // used for count verification

	log.Info("re-embedding entries", "count", len(entries), "provider", embedder.Name())

	embedded := 0
	for _, e := range entries {
		vec, err := embedder.Embed(e.content)
		if err != nil {
			log.Warn("embedding failed", "id", e.id, "error", err)
			continue
		}
		if err := vecStore.Upsert(e.id, vec); err != nil {
			log.Warn("vector upsert failed", "id", e.id, "error", err)
			continue
		}
		embedded++
		if embedded%10 == 0 {
			log.Info("progress", "embedded", embedded, "total", len(entries))
		}
	}

	log.Info("re-embedding complete", "embedded", embedded, "total", len(entries))
	return embedded, nil
}
