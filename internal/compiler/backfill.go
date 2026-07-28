package compiler

import (
	"bufio"
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

	// Pass 1: compiled articles, keyed the way the INDEX keys them — which is
	// not uniform across the three directories, and guessing from the filename
	// mints phantom documents that shadow the real ones:
	//   concepts/x.md   -> "concept:x"      (basename)
	//   outputs/x.md    -> "output:x.md"    (basename WITH the extension)
	//   summaries/x.md  -> the SOURCE PATH  (not derivable from the filename;
	//                      read from the summary's own `source:` frontmatter)
	type doc struct {
		id   string
		path string
	}
	var docs []doc

	scan := func(dir string, id func(name, path string) string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			full := filepath.Join(dir, e.Name())
			docID := id(e.Name(), full)
			if docID == "" {
				continue
			}
			docs = append(docs, doc{id: docID, path: full})
		}
	}

	scan(filepath.Join(projectDir, outputDir, "concepts"), func(name, _ string) string {
		return "concept:" + strings.TrimSuffix(name, ".md")
	})
	scan(filepath.Join(projectDir, outputDir, "outputs"), func(name, _ string) string {
		return "output:" + name
	})
	scan(filepath.Join(projectDir, outputDir, "summaries"), func(name, path string) string {
		src := summarySourcePath(path)
		if src == "" {
			log.Warn("reindex: summary has no source frontmatter — skipped, its chunks keep the old chunking",
				"summary", name)
			return ""
		}
		return src
	})
	res.Articles = len(docs)

	// Pass 2: raw sources already chunk-indexed as `src:<path>` (tier 1).
	// They have no article file, so they are re-chunked from the source
	// itself — the chunk index is the only record that they were indexed.
	srcIDs, err := chunkStore.ListDocIDs()
	if err != nil {
		// The command's whole contract is "the index now matches the config".
		// Continuing here would exit 0 having left every src: document at the
		// old chunking — the mixed index this exists to prevent.
		return res, fmt.Errorf("listing chunked documents: %w", err)
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

// summarySourcePath reads a summary's `source:` frontmatter — the source path
// IS the summary's document ID in the index (internal/wiki/reconcile.go:86).
func summarySourcePath(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if first {
			first = false
			if strings.TrimSpace(line) != "---" {
				return "" // no frontmatter block at all
			}
			continue
		}
		if strings.TrimSpace(line) == "---" {
			return "" // block closed without a source key
		}
		if rest, ok := strings.CutPrefix(line, "source:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
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
