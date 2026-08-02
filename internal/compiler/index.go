package compiler

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/extract/parsers"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/store"
)

// parserRegistry is the shared parser registry for code file parsing.
var parserRegistry = parsers.NewRegistry()

// indexRawSources indexes source files into FTS5 at Tier 0 (no embedding).
// Uses "src:" prefix on entry IDs to distinguish from compiled article entries.
// Skips sources that already have a compiled entry (non-prefixed) in FTS5.
func indexRawSources(projectDir string, sources []CompileItem, memStore store.EntryStore, items store.CompileItemStore, extractOpts ...extract.ExtractOpts) int {
	indexed := 0
	for _, src := range sources {
		// Skip if a compiled entry already exists (higher quality)
		if existing, _ := memStore.Get(src.SourcePath); existing != nil {
			if err := items.MarkPass(src.SourcePath, "indexed"); err != nil {
				log.Warn("mark pass failed", "path", src.SourcePath, "pass", "indexed", "error", err)
			}
			indexed++
			continue
		}

		absPath := filepath.Join(projectDir, src.SourcePath)
		content, err := extract.Extract(absPath, src.FileType, extractOpts...)
		if err != nil {
			log.Warn("tier 0 index: extract failed", "path", src.SourcePath, "error", err)
			if merr := items.MarkError(src.SourcePath, err); merr != nil {
				log.Warn("mark error failed", "path", src.SourcePath, "error", merr)
			}
			continue
		}

		// Parse code structure if supported
		entryContent := content.Text
		tags := []string{src.FileType, "tier:0"}
		ext := strings.TrimPrefix(filepath.Ext(src.SourcePath), ".")
		if parserRegistry.Supports(ext) {
			if pr, perr := parserRegistry.Parse(src.SourcePath, []byte(content.Text)); perr == nil && pr != nil {
				entryContent = content.Text + "\n\n---\nStructure:\n" + pr.Structure
				tags = append(tags, "parsed")
			}
		}

		memStore.Add(memory.Entry{
			ID:      "src:" + src.SourcePath,
			Content: entryContent,
			Tags:    tags,
		})
		sourcedate.RecordForSource(memStore, projectDir, src.SourcePath, "")

		if err := items.MarkPass(src.SourcePath, "indexed"); err != nil {
			log.Warn("mark pass failed", "path", src.SourcePath, "pass", "indexed", "error", err)
		}
		indexed++
	}
	return indexed
}

// indexApplyHookForTest is the fault-injection seam for the embed-pass
// cancel-during-apply witness (SPEC-04 D3, spec test 6). Called with each
// item's input index immediately BEFORE that item's apply step. Nil in
// production; same precedent as writeApplyHookForTest.
var indexApplyHookForTest func(idx int)

// embedPayload is everything the embed-pass apply phase needs, computed in
// the concurrent phase without touching any store (SPEC-04 D3).
type embedPayload struct {
	src             CompileItem
	extractErr      error // → MarkError in apply
	empty           bool  // content.Text == "" → skip entirely (today's behavior)
	chunks          []extract.Chunk
	chunkEmbeddings [][]float32
	allChunksOK     bool
}

// indexAndEmbedSources indexes + embeds sources at Tier 1.
// FTS5 indexing is synchronous; embedding uses BackpressureController for
// API rate limiting. Sources already indexed skip the FTS5 step.
//
// SPEC-04 D3 (deferred application): the fan-out only extracts + embeds
// (file reads, network calls). Every store mutation — chunk rows, vectors,
// cache invalidation, pass marks — happens in a sequential post-join apply
// loop in INPUT order, so rowid order never depends on completion order.
func indexAndEmbedSources(
	ctx context.Context,
	projectDir string,
	sources []CompileItem,
	memStore store.EntryStore,
	vecStore store.VectorStore,
	embedder embed.Embedder,
	items store.CompileItemStore,
	bp *BackpressureController,
	chunkStore store.ChunkStore,
	chunkSize int,
	chunkOverlap int,
	db store.DBHandle,
	extractOpts ...extract.ExtractOpts,
) (indexed, embedded int) {
	// Step 1: FTS5 index any sources not yet indexed
	for _, src := range sources {
		if src.PassIndexed {
			continue
		}

		// Skip if a compiled entry already exists
		if existing, _ := memStore.Get(src.SourcePath); existing != nil {
			if err := items.MarkPass(src.SourcePath, "indexed"); err != nil {
				log.Warn("mark pass failed", "path", src.SourcePath, "pass", "indexed", "error", err)
			}
			indexed++
			continue
		}

		absPath := filepath.Join(projectDir, src.SourcePath)
		content, err := extract.Extract(absPath, src.FileType, extractOpts...)
		if err != nil {
			log.Warn("tier 1 index: extract failed", "path", src.SourcePath, "error", err)
			if merr := items.MarkError(src.SourcePath, err); merr != nil {
				log.Warn("mark error failed", "path", src.SourcePath, "error", merr)
			}
			continue
		}

		// Parse code structure if supported
		entryContent := content.Text
		tags := []string{src.FileType, "tier:1"}
		ext := strings.TrimPrefix(filepath.Ext(src.SourcePath), ".")
		if parserRegistry.Supports(ext) {
			if pr, perr := parserRegistry.Parse(src.SourcePath, []byte(content.Text)); perr == nil && pr != nil {
				entryContent = content.Text + "\n\n---\nStructure:\n" + pr.Structure
				tags = append(tags, "parsed")
				if err := items.MarkPass(src.SourcePath, "parsed"); err != nil {
					log.Warn("mark pass failed", "path", src.SourcePath, "pass", "parsed", "error", err)
				}
			}
		}

		memStore.Add(memory.Entry{
			ID:      "src:" + src.SourcePath,
			Content: entryContent,
			Tags:    tags,
		})
		sourcedate.RecordForSource(memStore, projectDir, src.SourcePath, "")

		if err := items.MarkPass(src.SourcePath, "indexed"); err != nil {
			log.Warn("mark pass failed", "path", src.SourcePath, "pass", "indexed", "error", err)
		}
		indexed++
	}

	// Step 2: Embed (parallel via BackpressureController or fixed semaphore)
	if embedder == nil {
		return indexed, 0
	}

	if chunkSize <= 0 {
		chunkSize = 800
	}

	var wg sync.WaitGroup

	var applyList []*embedPayload

	for _, src := range sources {
		if src.PassEmbedded {
			continue
		}
		applyList = append(applyList, &embedPayload{src: src})
	}

	// Same bounded fallback as the write pass (Gate-2 review): a nil
	// backpressure controller must not mean unbounded goroutines.
	var sem chan struct{}
	if bp == nil {
		maxParallel := 20
		sem = make(chan struct{}, maxParallel)
	}

	for _, payload := range applyList {
		wg.Add(1)

		var release func()
		if bp != nil {
			release = bp.Acquire()
		} else {
			sem <- struct{}{}
			release = func() { <-sem }
		}

		go func(p *embedPayload) {
			defer wg.Done()
			defer release()

			s := p.src
			absPath := filepath.Join(projectDir, s.SourcePath)
			content, err := extract.Extract(absPath, s.FileType, extractOpts...)
			if err != nil {
				log.Warn("tier 1 embed: extract failed", "path", s.SourcePath, "error", err)
				p.extractErr = err
				return
			}

			if content.Text == "" {
				p.empty = true
				return
			}

			p.chunks = extract.ChunkText(content.Text, chunkSize, chunkOverlap)

			// Embed each chunk sequentially (same pattern as write.go:250-260)
			p.chunkEmbeddings = make([][]float32, len(p.chunks))
			p.allChunksOK = true
			for i, c := range p.chunks {
				vec, err := embedder.Embed(c.Text)
				if err != nil {
					p.allChunksOK = false
					if bp != nil && llm.IsRateLimitError(err) {
						delay := bp.OnRateLimit()
						log.Warn("embedding rate limited", "delay", delay)
					}
					log.Warn("tier 1 chunk embed failed", "path", s.SourcePath, "chunk", i, "error", err)
					continue
				}
				p.chunkEmbeddings[i] = vec
				if bp != nil {
					bp.OnSuccess()
				}
			}
		}(payload)
	}

	wg.Wait()

	// Apply phase: sequential, input order (SPEC-04 D3). A cancel observed
	// here stops the loop — remaining sources keep pass_embedded=0, so the
	// next compile's claim resumes them (P1-1).
	for i, p := range applyList {
		if indexApplyHookForTest != nil {
			indexApplyHookForTest(i)
		}
		if ctx != nil && ctx.Err() != nil {
			log.Warn("tier 1 embed: cancel observed during apply — remaining sources deferred to next compile", "applied", i, "total", len(applyList))
			break
		}
		s := p.src

		if p.extractErr != nil {
			if merr := items.MarkError(s.SourcePath, p.extractErr); merr != nil {
				log.Warn("mark error failed", "path", s.SourcePath, "error", merr)
			}
			continue
		}
		if p.empty {
			continue
		}

		docID := "src:" + s.SourcePath
		allChunksOK := p.allChunksOK

		if chunkStore != nil && db != nil {
			// Clean up legacy whole-document vector
			if err := vecStore.Delete(docID); err != nil {
				log.Warn("tier 1 legacy vector delete failed", "doc", docID, "error", err)
			}

			if err := db.WriteTx(func(tx *sql.Tx) error {
				if err := chunkStore.DeleteDocChunks(tx, docID); err != nil {
					return err
				}

				entries := make([]memory.ChunkEntry, len(p.chunks))
				for i, c := range p.chunks {
					entries[i] = memory.ChunkEntry{
						ChunkID:    fmt.Sprintf("%s:c%d", docID, i),
						ChunkIndex: c.Index,
						Heading:    c.Heading,
						Content:    c.Text,
					}
				}

				if err := chunkStore.IndexChunks(tx, docID, entries); err != nil {
					return err
				}

				for i, emb := range p.chunkEmbeddings {
					if emb != nil {
						if err := vecStore.UpsertChunk(tx, entries[i].ChunkID, docID, emb); err != nil {
							log.Warn("tier 1 chunk vector upsert failed", "chunk", entries[i].ChunkID, "error", err)
						}
					}
				}

				return nil
			}); err != nil {
				log.Error("tier 1 chunk indexing failed", "path", s.SourcePath, "error", err)
				if merr := items.MarkError(s.SourcePath, err); merr != nil {
					log.Warn("mark error failed", "path", s.SourcePath, "error", merr)
				}
				continue
			}
			vecStore.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit
		} else {
			// Fallback: single-vector embed (legacy path when chunk infra unavailable)
			if len(p.chunkEmbeddings) > 0 && p.chunkEmbeddings[0] != nil {
				if err := vecStore.Upsert(docID, p.chunkEmbeddings[0]); err != nil {
					log.Warn("tier 1 single-vector upsert failed", "doc", docID, "error", err)
				}
			} else {
				allChunksOK = false
			}
		}

		if allChunksOK {
			if err := items.MarkPass(s.SourcePath, "embedded"); err != nil {
				log.Warn("mark pass failed", "path", s.SourcePath, "pass", "embedded", "error", err)
			}
		}
		embedded++
	}
	return indexed, embedded
}
