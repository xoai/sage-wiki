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

	embedder := embed.NewFromConfig(cfg)
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
	total := len(entries)
	for i, e := range entries {
		vec, err := embedder.Embed(e.content)
		if err != nil {
			log.Warn("embedding failed", "progress", fmt.Sprintf("%d/%d", i+1, total), "id", e.id, "error", err)
			continue
		}
		if err := vecStore.Upsert(e.id, vec); err != nil {
			log.Warn("vector upsert failed", "id", e.id, "error", err)
			continue
		}
		embedded++
		log.Info("embedded", "progress", fmt.Sprintf("%d/%d", i+1, total), "id", e.id)
	}

	log.Info("re-embedding complete", "embedded", embedded, "total", len(entries))

	// Phase 2: re-embed chunk-level vectors so vec_chunks dimensions stay
	// consistent with the current embedding model. Skipping this leaves stale
	// chunks (e.g., from a prior 768-dim Ollama run) that break hybrid search
	// when their dim disagrees with the entry-level vectors.
	chunkRows, err := db.ReadDB().Query("SELECT chunk_id, doc_id, content FROM chunks_meta")
	if err != nil {
		return embedded, fmt.Errorf("re-embed: query chunks: %w", err)
	}
	defer chunkRows.Close()

	type chunk struct {
		chunkID string
		docID   string
		content string
	}
	var chunks []chunk
	for chunkRows.Next() {
		var c chunk
		if err := chunkRows.Scan(&c.chunkID, &c.docID, &c.content); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}

	if len(chunks) == 0 {
		return embedded, nil
	}

	log.Info("re-embedding chunks", "count", len(chunks), "provider", embedder.Name())

	tx, err := db.WriteDB().Begin()
	if err != nil {
		return embedded, fmt.Errorf("re-embed: begin chunk tx: %w", err)
	}
	chunkOK := 0
	for i, c := range chunks {
		vec, err := embedder.Embed(c.content)
		if err != nil {
			log.Warn("chunk embedding failed", "progress", fmt.Sprintf("%d/%d", i+1, len(chunks)), "chunk", c.chunkID, "error", err)
			continue
		}
		if err := vecStore.UpsertChunk(tx, c.chunkID, c.docID, vec); err != nil {
			log.Warn("chunk upsert failed", "chunk", c.chunkID, "error", err)
			continue
		}
		chunkOK++
	}
	if err := tx.Commit(); err != nil {
		return embedded, fmt.Errorf("re-embed: commit chunks: %w", err)
	}

	log.Info("chunk re-embedding complete", "embedded", chunkOK, "total", len(chunks))
	return embedded + chunkOK, nil
}
