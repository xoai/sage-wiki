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
	"github.com/xoai/sage-wiki/internal/store"
)

// BackfillResult reports what a chunk-index rebuild covered, per doc family.
// Callers surface it so "reindex said 12 articles" can be checked against
// what the wiki actually holds.
type BackfillResult struct {
	Articles int // concepts/, summaries/, outputs/ — files on disk
	Sources  int // src: docs, re-chunked from their source files
}

// Total returns the number of documents re-chunked.
func (r BackfillResult) Total() int { return r.Articles + r.Sources }

// BackfillChunks rebuilds the chunk index from the documents on disk, at the
// caller's chunkSize/chunkOverlap. It covers EVERY doc family that carries
// chunks — compiled articles (concepts/, summaries/, outputs/) and raw
// sources indexed as `src:<path>` — because a partial rebuild would leave the
// index mixing two chunkings, which is exactly what the chunk-overlap config
// contract forbids (spec §2.5).
//
// Each document is replaced in one transaction (delete-then-insert), so a
// crash mid-run leaves earlier documents rebuilt and later ones untouched,
// never a half-written document.
//
// A nil embedder rebuilds the text index only: DeleteDocChunks also drops the
// document's chunk vectors, and without an embedder there is nothing to put
// back. Callers must treat a nil embedder as "chunk vectors will be dropped"
// and say so — `sage-wiki reindex` refuses unless the user asked for it.
func BackfillChunks(projectDir string, outputDir string, chunkSize int, chunkOverlap int,
	chunkStore store.ChunkStore, vecStore store.VectorStore,
	embedder embed.Embedder, db store.DBHandle) (BackfillResult, error) {

	if chunkSize <= 0 {
		chunkSize = 800
	}

	var res BackfillResult

	// Pass 1: compiled articles, keyed by their output directory.
	dirs := []struct {
		path   string
		prefix string
	}{
		{filepath.Join(projectDir, outputDir, "concepts"), "concept:"},
		{filepath.Join(projectDir, outputDir, "summaries"), "summary:"},
		{filepath.Join(projectDir, outputDir, "outputs"), "output:"},
	}

	type doc struct {
		id   string
		path string
	}
	var docs []doc
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			docs = append(docs, doc{
				id:   dir.prefix + strings.TrimSuffix(e.Name(), ".md"),
				path: filepath.Join(dir.path, e.Name()),
			})
		}
	}
	res.Articles = len(docs)

	// Pass 2: raw sources already chunk-indexed as `src:<path>` (tier 1).
	// They have no article file, so they are re-chunked from the source
	// itself — the chunk index is the only record that they were indexed.
	srcIDs, err := chunkStore.ListDocIDs()
	if err != nil {
		log.Warn("backfill: listing chunked docs failed — source chunks keep their old chunking", "error", err)
	}
	for _, id := range srcIDs {
		if !strings.HasPrefix(id, "src:") {
			continue
		}
		docs = append(docs, doc{id: id, path: filepath.Join(projectDir, strings.TrimPrefix(id, "src:"))})
		res.Sources++
	}

	if len(docs) == 0 {
		return res, nil
	}

	log.Info("rebuilding chunk index", "articles", res.Articles, "sources", res.Sources,
		"chunk_size", chunkSize, "chunk_overlap", chunkOverlap, "vectors", embedder != nil)

	count := 0
	for _, d := range docs {
		text, err := docText(d.id, d.path)
		if err != nil {
			log.Warn("backfill: read failed", "doc", d.id, "path", d.path, "error", err)
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}

		chunks := extract.ChunkText(text, chunkSize, chunkOverlap)

		// Embed chunks outside transaction
		var chunkEmbeddings [][]float32
		if embedder != nil {
			chunkEmbeddings = make([][]float32, len(chunks))
			for i, c := range chunks {
				vec, err := embedder.Embed(c.Text)
				if err != nil {
					log.Warn("backfill: chunk embed failed — that chunk loses its vector",
						"doc", d.id, "chunk", i, "error", err)
					continue
				}
				chunkEmbeddings[i] = vec
			}
		}

		// Single WriteTx per document (delete first for idempotency)
		if err := db.WriteTx(func(tx *sql.Tx) error {
			if err := chunkStore.DeleteDocChunks(tx, d.id); err != nil {
				return err
			}
			chunkEntries := make([]memory.ChunkEntry, len(chunks))
			for i, c := range chunks {
				chunkEntries[i] = memory.ChunkEntry{
					ChunkID:    fmt.Sprintf("%s:c%d", d.id, i),
					ChunkIndex: c.Index,
					Heading:    c.Heading,
					Content:    c.Text,
				}
			}

			if err := chunkStore.IndexChunks(tx, d.id, chunkEntries); err != nil {
				return err
			}

			if chunkEmbeddings != nil {
				for i, emb := range chunkEmbeddings {
					if emb != nil {
						if err := vecStore.UpsertChunk(tx, chunkEntries[i].ChunkID, d.id, emb); err != nil {
							log.Warn("backfill: chunk vector upsert failed", "chunk", chunkEntries[i].ChunkID, "error", err)
						}
					}
				}
			}
			return nil
		}); err != nil {
			log.Warn("backfill: chunk indexing failed", "doc", d.id, "error", err)
			continue
		}
		vecStore.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit

		count++
		if count%50 == 0 {
			log.Info("backfill progress", "done", count, "total", len(docs))
		}
	}

	log.Info("chunk index rebuild complete", "indexed", count, "documents", len(docs))
	return res, nil
}

// docText reads a document's text. Articles are markdown read verbatim (the
// historical behavior — frontmatter included, so chunk text matches what the
// article file holds); sources go through the extractor, the same way tier-1
// indexing produced their chunks in the first place.
func docText(docID, path string) (string, error) {
	if strings.HasPrefix(docID, "src:") {
		content, err := extract.Extract(path, "")
		if err != nil {
			return "", err
		}
		return content.Text, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
