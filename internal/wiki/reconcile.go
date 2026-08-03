package wiki

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
	"sort"
)

// ReconcileResult summarizes a reconcile pass.
type ReconcileResult struct {
	Reindexed       int // outputs re-indexed (file-no-DB or changed)
	Dropped         int // DB entries dropped (file vanished / orphaned)
	VectorsDeferred int // re-indexed without vectors because the embedder was offline
}

// ReconcileBackend is the primary, backend-neutral entry (P3-7): it heals
// file <-> DB drift at startup (REL-03 / D5) on whichever backend the vault
// is configured with — the P2-1 skip-list's sqlite-forced reconcile is gone.
// The manifest is the authoritative "what should exist" set; for each
// expected output it makes the DB consistent with the on-disk file:
//   - file present, not indexed        -> re-index (crash between file and index)
//   - file present, content changed    -> re-index (detected against output_index)
//   - file absent, still indexed       -> drop the DB rows
//   - already indexed, hash unrecorded -> record the hash only (no re-embed)
//
// The scan is lock-free; each individual repair takes the manifest lock so it is
// cross-process safe (D5). Re-index uses delete-then-insert and writes the
// output-hash completion signal LAST — so a crash mid-repair leaves the output
// out of FTS and is simply re-detected next run. With no embedder (offline
// launch) it reconciles FTS/chunks/ontology and defers vectors.
func ReconcileBackend(ctx context.Context, projectDir string, cfg *config.Config, b store.Backend, embedder embed.Embedder) (*ReconcileResult, error) {
	// The interface permits nil accessors (cli_queue_test's fake exercises
	// exactly that); a nil Trust/OutputIndex would panic mid-run instead of
	// erroring. Both real backends return non-nil; the check is the boundary
	// guard, not a behavior change.
	if b.Trust() == nil || b.OutputIndex() == nil {
		return nil, fmt.Errorf("reconcile: backend %T must provide Trust and OutputIndex stores", b)
	}
	rc := &reconciler{
		projectDir:   projectDir,
		manifestPath: filepath.Join(projectDir, ".manifest.json"),
		outputRel:    cfg.Output,
		chunkSize:    cfg.Search.ChunkSizeOrDefault(),
		chunkOverlap: cfg.Search.ChunkOverlapOrDefault(),
		writeTx:      b,
		trustStore:   b.Trust(),
		mem:          b.Entries(),
		vec:          b.Vectors(),
		chunks:       b.Chunks(),
		ont:          b.Ontology(),
		oi:           b.OutputIndex(),
		embedder:     embedder,
		res:          &ReconcileResult{},
	}
	return rc.run(ctx)
}

// Reconcile is the legacy sqlite entry, kept so existing callers and tests
// are byte-identical: it builds the same concrete SQLite stores it always
// did (they satisfy the reconciler's interface fields directly — no backend
// wrap needed, which also keeps wiki out of the
// wiki→sqlitestore→compiler import cycle).
func Reconcile(ctx context.Context, projectDir string, cfg *config.Config, db store.DBHandle, embedder embed.Embedder) (*ReconcileResult, error) {
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	rc := &reconciler{
		projectDir:   projectDir,
		manifestPath: filepath.Join(projectDir, ".manifest.json"),
		outputRel:    cfg.Output,
		chunkSize:    cfg.Search.ChunkSizeOrDefault(),
		chunkOverlap: cfg.Search.ChunkOverlapOrDefault(),
		writeTx:      db,
		trustStore:   trust.NewStore(db),
		mem:          memory.NewStore(db),
		vec:          vectors.NewStore(db),
		chunks:       memory.NewChunkStore(db),
		ont: ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC)),
		oi:       storage.NewOutputIndex(db),
		embedder: embedder,
		res:      &ReconcileResult{},
	}
	return rc.run(ctx)
}

type reconciler struct {
	projectDir   string
	manifestPath string
	outputRel    string
	chunkSize    int
	chunkOverlap int
	writeTx      store.DBHandle
	trustStore   store.TrustStore
	mem          store.EntryStore
	vec          store.VectorStore
	chunks       store.ChunkStore
	ont          store.OntologyStore
	oi           store.OutputIndexStore
	embedder     embed.Embedder
	res          *ReconcileResult
}

// expectedOutput is one output file the manifest says should exist and be indexed.
type expectedOutput struct {
	path  string // project-relative output file path
	kind  string // "article" | "summary"
	ftsID string // FTS/vector/chunk doc id: "concept:<name>" (article) or the source path (summary)
	name  string // concept name (article only)
}

func (rc *reconciler) run(ctx context.Context) (*ReconcileResult, error) {
	// Read the manifest lock-free (atomic Save guarantees a consistent snapshot).
	mf, err := manifest.Load(rc.manifestPath)
	if err != nil {
		return nil, fmt.Errorf("reconcile: load manifest: %w", err)
	}
	mf.SetNow(config.NowUTC)

	expected := rc.expectedOutputs(mf)
	seen := make(map[string]bool, len(expected))
	for _, eo := range expected {
		seen[eo.path] = true
		if err := rc.reconcileOne(ctx, eo); err != nil {
			log.Warn("reconcile: heal failed", "output", eo.path, "error", err)
		}
	}

	// Backfill entry_dates for pre-V13 corpora (ADR-039). Additive and
	// idempotent — dated entries are untouched, dateless chains stay absent.
	if n, err := sourcedate.Backfill(rc.projectDir, rc.mem, mf); err != nil {
		log.Warn("reconcile: source-date backfill failed", "error", err)
	} else if n > 0 {
		log.Info("reconcile: source dates backfilled", "count", n)
	}
	// Pre-existing promoted Q&A outputs date from their trust records.
	if n, err := sourcedate.BackfillOutputs(rc.mem, rc.trustStore); err != nil {
		log.Warn("reconcile: output-date backfill failed", "error", err)
	} else if n > 0 {
		log.Info("reconcile: output dates backfilled", "count", n)
	}

	// output_index rows that are no longer an expected output are orphaned.
	if rows, err := rc.oi.All(); err == nil {
		for path := range rows {
			if seen[path] {
				continue
			}
			if err := rc.dropOrphanRow(ctx, path); err != nil {
				log.Warn("reconcile: drop orphan failed", "output", path, "error", err)
			}
		}
	}

	if rc.res.Reindexed > 0 || rc.res.Dropped > 0 {
		log.Info("reconcile complete",
			"reindexed", rc.res.Reindexed, "dropped", rc.res.Dropped, "vectors_deferred", rc.res.VectorsDeferred)
	}
	return rc.res, nil
}

func (rc *reconciler) expectedOutputs(mf *manifest.Manifest) []expectedOutput {
	var out []expectedOutput
	for name, c := range mf.Concepts {
		if c.ArticlePath == "" {
			continue
		}
		out = append(out, expectedOutput{path: c.ArticlePath, kind: "article", ftsID: "concept:" + name, name: name})
	}
	for src, s := range mf.Sources {
		if s.SummaryPath == "" {
			continue
		}
		out = append(out, expectedOutput{path: s.SummaryPath, kind: "summary", ftsID: src})
	}
	// SPEC-04 D1: heal/replace order is observable (FTS delete+re-add rowids).
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func (rc *reconciler) reconcileOne(ctx context.Context, eo expectedOutput) error {
	data, readErr := os.ReadFile(filepath.Join(rc.projectDir, eo.path))

	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return fmt.Errorf("read output: %w", readErr)
		}
		// File gone — drop the DB rows if it was indexed.
		ftsEntry, _ := rc.mem.Get(eo.ftsID)
		_, hasRow, _ := rc.oi.Get(eo.path)
		if ftsEntry != nil || hasRow {
			return manifest.WithLock(ctx, rc.manifestPath, func() error { return rc.drop(eo) })
		}
		return nil // never indexed, no file — nothing to reconcile.
	}

	hash := storage.HashBytes(data)
	oiHash, hasRow, _ := rc.oi.Get(eo.path)

	// "Processed" means an ONLINE reconcile already handled this exact content and
	// recorded its hash. We then skip it — even if some chunk embeds failed —
	// rather than re-index it every startup (thrash). A content change (hash
	// mismatch) reopens processing. Offline reconciles never mark content
	// processed, so a later online pass completes them.
	if hasRow && oiHash == hash {
		return nil
	}

	// Not processed. Consult ACTUAL DB state, not just output_index. A live compile
	// that re-indexed this output (updating FTS/vectors but not output_index) must
	// NOT be re-embedded just because its recorded hash lagged — otherwise a full
	// recompile would re-embed the whole vault. The FTS content is the truth for
	// "is the index current"; a missing chunk vector is the truth for "vectors
	// deferred".
	ftsEntry, _ := rc.mem.Get(eo.ftsID)
	switch {
	case ftsEntry == nil:
		return rc.lockedReindex(ctx, eo, data, hash) // present on disk, not indexed
	case ftsEntry.Content != rc.indexText(eo, data):
		return rc.lockedReindex(ctx, eo, data, hash) // content changed, index stale
	case !rc.vectorsOK(eo):
		return rc.lockedReindex(ctx, eo, data, hash) // indexed & current, vectors missing → fill
	default:
		// Consistent: the DB reflects the file. Mark the content processed by
		// recording its hash — but only when ONLINE, so an offline pass leaves it
		// unprocessed for a later online pass to fill its vectors.
		if rc.embedder != nil {
			return manifest.WithLock(ctx, rc.manifestPath, func() error { return rc.oi.Set(eo.path, hash) })
		}
		return nil
	}
}

// vectorsOK reports whether the output's vectors are complete. Only concept
// articles use chunk vectors uniformly (compiler and reconciler), so vector
// completeness is enforced for them when an embedder is available; summaries are
// left to the compile's own (whole-doc) vector handling. Offline (no embedder)
// is always "ok" so an offline reconcile is idempotent rather than looping.
func (rc *reconciler) vectorsOK(eo expectedOutput) bool {
	if rc.embedder == nil || eo.kind != "article" {
		return true
	}
	has, err := rc.vec.HasChunkVectors(eo.ftsID)
	return err != nil || has // on query error, don't force a re-embed loop
}

// indexText is the text the reconciler indexes into FTS, matching what the
// compiler stores: the full article for a concept, the frontmatter-stripped body
// for a summary (the compile indexes the summary body, not its frontmatter).
func (rc *reconciler) indexText(eo expectedOutput, data []byte) string {
	if eo.kind == "summary" {
		return stripFrontmatter(string(data))
	}
	return string(data)
}

// lockedReindex computes embeddings OUTSIDE the manifest lock (network calls must
// not block other writers), then applies the store writes under the lock.
func (rc *reconciler) lockedReindex(ctx context.Context, eo expectedOutput, data []byte, hash string) error {
	indexText := rc.indexText(eo, data)
	chunks := extract.ChunkText(indexText, rc.chunkSize, rc.chunkOverlap)

	deferVec := rc.embedder == nil
	var embs [][]float32
	if !deferVec {
		embs = make([][]float32, len(chunks))
		for i, c := range chunks {
			if v, err := rc.embedder.Embed(c.Text); err == nil {
				embs[i] = v
			}
		}
	}
	return manifest.WithLock(ctx, rc.manifestPath, func() error {
		return rc.applyReindex(eo, indexText, hash, chunks, embs, deferVec)
	})
}

// applyReindex writes the index across every store, then records the output hash
// LAST as the completion signal. FTS uses delete-then-insert so a crash before
// completion leaves the output out of FTS and is re-detected next run.
func (rc *reconciler) applyReindex(eo expectedOutput, indexText, hash string, chunks []extract.Chunk, embs [][]float32, deferVec bool) error {
	// FTS (delete-then-insert). Preserve the existing entry's richer tags (the
	// compile stores entityType + aliases for articles, the resolved source type
	// for summaries) when re-indexing a changed output; fall back to the generic
	// kind tag only when there is no prior entry (a first-time file-no-DB heal).
	// A failed Delete surfaces as a duplicate-insert error from Add below.
	tags := []string{eo.kind}
	if existing, _ := rc.mem.Get(eo.ftsID); existing != nil && len(existing.Tags) > 0 {
		tags = existing.Tags
	}
	_ = rc.mem.Delete(eo.ftsID)
	if err := rc.mem.Add(memory.Entry{ID: eo.ftsID, Content: indexText, ArticlePath: eo.path, Tags: tags}); err != nil {
		return fmt.Errorf("reindex FTS: %w", err)
	}
	if eo.kind == "article" {
		// Type and display name come from the article, not from a constant:
		// AddEntity writes `type` unconditionally (P3-1), so hard-coding
		// TypeConcept here would demote a `technique` on every reindex, and a
		// raw-slug Name would overwrite the article's display name.
		if err := rc.ont.AddEntity(ontology.Entity{
			ID:          eo.name,
			Type:        ontology.ArticleEntityType(indexText, rc.ont),
			Name:        ontology.FormatConceptName(eo.name),
			ArticlePath: eo.path,
		}); err != nil {
			return fmt.Errorf("reindex ontology: %w", err)
		}
	}

	// Chunks + chunk-vectors (delete-then-insert); clear any legacy whole-doc vector.
	docID := eo.ftsID
	_ = rc.vec.Delete(docID)
	_ = rc.vec.DeleteDocChunkVectors(docID)
	if err := rc.writeTx.WriteTx(func(tx *sql.Tx) error {
		if err := rc.chunks.DeleteDocChunks(tx, docID); err != nil {
			return err
		}
		entries := make([]memory.ChunkEntry, len(chunks))
		for i, c := range chunks {
			entries[i] = memory.ChunkEntry{
				ChunkID:    fmt.Sprintf("%s:c%d", docID, i),
				ChunkIndex: c.Index,
				Heading:    c.Heading,
				Content:    c.Text,
			}
		}
		if err := rc.chunks.IndexChunks(tx, docID, entries); err != nil {
			return err
		}
		if !deferVec {
			for i, emb := range embs {
				if emb != nil {
					if err := rc.vec.UpsertChunk(tx, entries[i].ChunkID, docID, emb); err != nil {
						log.Warn("reconcile: chunk vector upsert failed", "chunk", entries[i].ChunkID, "error", err)
					}
				}
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("reindex chunks: %w", err)
	}
	rc.vec.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit

	// Completion signal LAST — recorded only when ONLINE (an embedder was
	// available), so an offline reindex stays "unprocessed" and a later online
	// reconcile fills its vectors. An online attempt that produced no vectors
	// (all embeds failed) still records the hash, so it is not re-tried every
	// startup; a content change reopens it.
	if !deferVec {
		if err := rc.oi.Set(eo.path, hash); err != nil {
			return fmt.Errorf("reindex record hash: %w", err)
		}
	}

	rc.res.Reindexed++
	if deferVec {
		rc.res.VectorsDeferred++
		log.Info("reconcile: re-indexed with vectors deferred (embedder offline)", "output", eo.path)
	}
	return nil
}

// stripFrontmatter removes a leading `---\n...\n---\n` YAML frontmatter block,
// matching how the compiler indexes a summary's body (not its frontmatter) into
// FTS, so the reconciler's content comparison lines up.
func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return s // malformed — leave as-is
	}
	body := rest[end+len("\n---\n"):]
	// The compiler writes "...---\n\n" + body, so exactly ONE separator newline
	// precedes the body. Strip that one only — TrimLeft would also eat a leading
	// newline that is genuinely part of the body, causing permanent re-index churn.
	return strings.TrimPrefix(body, "\n")
}

func (rc *reconciler) drop(eo expectedOutput) error {
	return rc.dropByID(eo.ftsID, eo.kind == "article", eo.name, eo.path)
}

func (rc *reconciler) dropByID(ftsID string, isArticle bool, name, outputPath string) error {
	// Best-effort deletes across stores; a partial drop self-heals because the
	// next reconcile still sees the missing file and re-drops the remnants. The
	// output_index delete is the last, checked step (its row is the drift signal).
	_ = rc.mem.Delete(ftsID)
	_ = rc.vec.Delete(ftsID)
	_ = rc.vec.DeleteDocChunkVectors(ftsID)
	if err := rc.writeTx.WriteTx(func(tx *sql.Tx) error {
		return rc.chunks.DeleteDocChunks(tx, ftsID)
	}); err != nil {
		log.Warn("reconcile: drop chunks failed", "doc", ftsID, "error", err)
	} else {
		rc.vec.InvalidateChunkCache() // chunk cache invalidation (P1-5): caller-tx writes are invisible to vectors.Store until post-commit
	}
	if isArticle && name != "" {
		_ = rc.ont.DeleteEntity(name)
	}
	if err := rc.oi.Delete(outputPath); err != nil {
		return err
	}
	rc.res.Dropped++
	return nil
}

// dropOrphanRow removes an output_index row (and derivable DB rows) for a path
// that is no longer an expected output. A concept path yields its FTS/ontology
// id; a summary path does not (its FTS id is the source path, not derivable from
// the filename), so only the recorded hash is dropped — the summary's FTS/vector
// cleanup for a removed source is handled by the compile's handleRemovedSources.
func (rc *reconciler) dropOrphanRow(ctx context.Context, path string) error {
	conceptsPrefix := filepath.ToSlash(filepath.Join(rc.outputRel, "concepts")) + "/"
	p := filepath.ToSlash(path)
	return manifest.WithLock(ctx, rc.manifestPath, func() error {
		if strings.HasPrefix(p, conceptsPrefix) && strings.HasSuffix(p, ".md") {
			name := strings.TrimSuffix(filepath.Base(path), ".md")
			return rc.dropByID("concept:"+name, true, name, path)
		}
		// Summary/unknown path: we can't derive the FTS id (a summary's id is its
		// source path), so we only prune the stale output_index cache row — not a
		// real DB-row drop, so it does not count toward Dropped. The summary's
		// FTS/vector rows for a removed source are cleaned by the compile's
		// handleRemovedSources when the source leaves the manifest.
		log.Info("reconcile: pruning stale output_index row (non-concept orphan)", "output", path)
		return rc.oi.Delete(path)
	})
}
