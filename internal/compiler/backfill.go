package compiler

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// BackfillChunks scans existing articles and indexes them at chunk level.
// This is called once after migration to populate the chunk index without
// requiring a full recompile.
func BackfillChunks(projectDir string, outputDir string, chunkSize int,
	chunkStore *memory.ChunkStore, vecStore *vectors.Store,
	embedder embed.Embedder, db *storage.DB) error {

	if chunkSize <= 0 {
		chunkSize = 800
	}

	// Scan concepts and summaries directories
	dirs := []struct {
		path   string
		prefix string
	}{
		{filepath.Join(projectDir, outputDir, "concepts"), "concept:"},
		{filepath.Join(projectDir, outputDir, "summaries"), "summary:"},
	}

	var total int
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			total++
		}
	}

	if total == 0 {
		return nil
	}

	log.Info("backfilling chunk index", "articles", total)

	count := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}

			name := strings.TrimSuffix(e.Name(), ".md")
			docID := dir.prefix + name
			absPath := filepath.Join(dir.path, e.Name())

			data, err := os.ReadFile(absPath)
			if err != nil {
				log.Warn("backfill: read failed", "path", absPath, "error", err)
				continue
			}

			text := string(data)
			chunks := extract.ChunkText(text, chunkSize)

			// Embed chunks outside transaction
			var chunkEmbeddings [][]float32
			if embedder != nil {
				chunkEmbeddings = make([][]float32, len(chunks))
				for i, c := range chunks {
					vec, err := embedder.Embed(c.Text)
					if err != nil {
						continue
					}
					chunkEmbeddings[i] = vec
				}
			}

			// Single WriteTx for each article (delete first for idempotency)
			if err := db.WriteTx(func(tx *sql.Tx) error {
				if err := chunkStore.DeleteDocChunks(tx, docID); err != nil {
					return err
				}
				chunkEntries := make([]memory.ChunkEntry, len(chunks))
				for i, c := range chunks {
					chunkEntries[i] = memory.ChunkEntry{
						ChunkID:    fmt.Sprintf("%s:c%d", docID, i),
						ChunkIndex: c.Index,
						Heading:    c.Heading,
						Content:    c.Text,
					}
				}

				if err := chunkStore.IndexChunks(tx, docID, chunkEntries); err != nil {
					return err
				}

				if chunkEmbeddings != nil {
					for i, emb := range chunkEmbeddings {
						if emb != nil {
							if err := vecStore.UpsertChunk(tx, chunkEntries[i].ChunkID, docID, emb); err != nil {
								log.Warn("backfill: chunk vector upsert failed", "chunk", chunkEntries[i].ChunkID, "error", err)
							}
						}
					}
				}
				return nil
			}); err != nil {
				log.Warn("backfill: chunk indexing failed", "doc", docID, "error", err)
				continue
			}

			count++
			if count%50 == 0 {
				log.Info("backfill progress", "done", count, "total", total)
			}
		}
	}

	log.Info("chunk index backfill complete", "indexed", count)
	return nil
}
