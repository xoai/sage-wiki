package compiler

import (
	"fmt"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// ReEmbed regenerates vector embeddings for all FTS5 entries
// without re-summarizing or recompiling. backend may be nil (legacy sqlite
// open); when set, the caller retains ownership (P2-1 T16).
func ReEmbed(projectDir string, backend store.Backend) (int, error) {
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return 0, fmt.Errorf("re-embed: load config: %w", err)
	}

	embedder := embed.NewFromConfig(cfg)
	if embedder == nil {
		return 0, fmt.Errorf("re-embed: no embedding provider available")
	}

	// compiler cannot import storedial (cycle) — the Backend is injected by
	// leaf callers; nil keeps the legacy sqlite open byte-identical.
	var db store.DBHandle
	if backend != nil {
		db = backend
		defer func() {}() // caller owns the Backend
	} else {
		sdb, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
		if err != nil {
			return 0, fmt.Errorf("re-embed: open db: %w", err)
		}
		defer sdb.Close()
		db = sdb
	}

	var memStore store.EntryStore
	var vecStore store.VectorStore
	var chunkStore store.ChunkStore
	if backend != nil {
		memStore = backend.Entries()
		vecStore = backend.Vectors()
		chunkStore = backend.Chunks()
	} else {
		memStore = memory.NewStore(db)
		vecStore = vectors.NewStore(db)
		chunkStore = memory.NewChunkStore(db)
	}

	// Get all FTS5 entries (P2-1: via the EntryStore seam)
	storeEntries, err := memStore.ListAll()
	if err != nil {
		return 0, fmt.Errorf("re-embed: query entries: %w", err)
	}

	type entry struct {
		id      string
		content string
	}
	var entries []entry
	for _, e := range storeEntries {
		entries = append(entries, entry{id: e.ID, content: e.Content})
	}

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
	storeChunks, err := chunkStore.ListAll()
	if err != nil {
		return embedded, fmt.Errorf("re-embed: query chunks: %w", err)
	}

	type chunk struct {
		chunkID string
		docID   string
		content string
	}
	var chunks []chunk
	for _, c := range storeChunks {
		chunks = append(chunks, chunk{chunkID: c.ChunkID, docID: c.DocID, content: c.Content})
	}

	if len(chunks) == 0 {
		return embedded, nil
	}

	log.Info("re-embedding chunks", "count", len(chunks), "provider", embedder.Name())

	// P2-1: BeginWrite holds the write mutex for the tx duration (parity
	// with the single-write-connection world — design D9); Commit/Rollback
	// releases it.
	tx, err := db.BeginWrite()
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
		if err := vecStore.UpsertChunk(tx.Tx, c.chunkID, c.docID, vec); err != nil {
			log.Warn("chunk upsert failed", "chunk", c.chunkID, "error", err)
			continue
		}
		chunkOK++
	}
	if err := tx.Commit(); err != nil {
		return embedded, fmt.Errorf("re-embed: commit chunks: %w", err)
	}
	vecStore.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit

	log.Info("chunk re-embedding complete", "embedded", chunkOK, "total", len(chunks))
	return embedded + chunkOK, nil
}
