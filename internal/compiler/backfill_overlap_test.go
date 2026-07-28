package compiler

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// backfillEmbedder is a deterministic non-nil embedder.
type backfillEmbedder struct{}

func (backfillEmbedder) Embed(string) ([]float32, error) { return []float32{0.1, 0.2, 0.3}, nil }
func (backfillEmbedder) Dimensions() int                 { return 3 }
func (backfillEmbedder) Name() string                    { return "stub" }

// writeLongArticle drops a concept article that splits into several chunks
// at a small chunk budget.
func writeLongArticle(t *testing.T, projectDir, outputDir, name string) {
	t.Helper()
	dir := filepath.Join(projectDir, outputDir, "concepts")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("Retrieval quality depends on where the chunk boundary falls, ")
		b.WriteString("and a fact that straddles one is easy to lose entirely.\n\n")
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}
}

// The chunk-overlap config takes effect only when the chunk index is rebuilt
// (spec §2.5): changing the value does not retroactively re-chunk anything,
// and the explicit reindex — here BackfillChunks, the same delete-then-insert
// path a reindex runs — is what applies it.
func TestChunkOverlapAppliedOnlyOnReindex(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	const outputDir = "wiki"
	writeLongArticle(t, projectDir, outputDir, "boundaries")

	chunkStore := memory.NewChunkStore(db)
	vecStore := vectors.NewStore(db)

	// Index once with the default (overlap 0).
	if _, err := BackfillChunks(projectDir, outputDir, 100, 0, chunkStore, vecStore, nil, db); err != nil {
		t.Fatalf("backfill (overlap 0): %v", err)
	}
	before, err := chunkStore.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) < 2 {
		t.Fatalf("indexed %d chunks, want >= 2", len(before))
	}

	// A config change alone changes nothing on disk — the stored chunks are
	// still the overlap-0 chunks until something rebuilds them.
	unchanged, err := chunkStore.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged) != len(before) {
		t.Fatalf("chunk count changed without a reindex: %d, want %d", len(unchanged), len(before))
	}
	for i := range before {
		if unchanged[i].Content != before[i].Content {
			t.Fatalf("chunk %d content changed without a reindex", i)
		}
	}

	// The explicit reindex applies the new overlap.
	if _, err := BackfillChunks(projectDir, outputDir, 100, 20, chunkStore, vecStore, nil, db); err != nil {
		t.Fatalf("backfill (overlap 20): %v", err)
	}
	after, err := chunkStore.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("reindex changed chunk count: %d, want %d (delete-then-insert, no duplicates)", len(after), len(before))
	}
	if after[0].Content != before[0].Content {
		t.Error("first chunk should be unchanged — no predecessor to overlap")
	}

	grew := false
	for i := 1; i < len(after); i++ {
		if !strings.HasSuffix(after[i].Content, before[i].Content) {
			t.Fatalf("chunk %d no longer ends with its original text", i)
		}
		if len(after[i].Content) > len(before[i].Content) {
			grew = true
			prefix := strings.TrimSuffix(strings.TrimSuffix(after[i].Content, before[i].Content), "\n")
			if !strings.HasSuffix(before[i-1].Content, prefix) {
				t.Errorf("chunk %d prefix %q is not a tail of chunk %d", i, prefix, i-1)
			}
		}
	}
	if !grew {
		t.Error("reindex with overlap 20 produced no overlapping chunks")
	}
}

// A rebuild covers every doc family that carries chunks — and must key each
// one the way the INDEX keys it. The three directories do NOT share a
// convention (concepts by basename, outputs by basename WITH the extension,
// summaries by their source path), and guessing from the filename mints
// phantom documents: the real doc keeps its old chunking while a duplicate
// copy of the same text appears under an ID no entry row backs, surfacing on
// every adapter with a blank article path.
func TestBackfillKeysEveryDocFamilyLikeTheIndex(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	const outputDir = "wiki"
	writeLongArticle(t, projectDir, outputDir, "boundaries")

	// An answer auto-filed back into the wiki: indexed as "output:<file.md>".
	outDir := filepath.Join(projectDir, outputDir, "outputs")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "answered.md"),
		[]byte("A generated answer about chunk boundaries and retrieval."), 0644); err != nil {
		t.Fatal(err)
	}

	// A summary: indexed under its SOURCE path, which only its frontmatter knows.
	sumDir := filepath.Join(projectDir, outputDir, "summaries")
	if err := os.MkdirAll(sumDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sumDir, "sources-paper.md"),
		[]byte("---\nsource: sources/paper.pdf\nsource_type: article\n---\n\nSummary of the paper about chunk boundaries."), 0644); err != nil {
		t.Fatal(err)
	}

	// A raw source that tier-1 indexing already chunked (no article file).
	if err := os.MkdirAll(filepath.Join(projectDir, "raw"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "raw", "note.md"),
		[]byte("Raw note about chunk boundaries in the source corpus."), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-seed the chunk rows the compile pipeline would have written, under
	// the REAL doc IDs, so a phantom-ID rebuild is detectable: the real rows
	// would survive untouched.
	chunkStore := memory.NewChunkStore(db)
	vecStore := vectors.NewStore(db)
	realIDs := []string{"output:answered.md", "sources/paper.pdf", "src:raw/note.md"}
	if err := db.WriteTx(func(tx *sql.Tx) error {
		for _, id := range realIDs {
			if err := chunkStore.IndexChunks(tx, id, []memory.ChunkEntry{
				{ChunkID: id + ":c0", ChunkIndex: 0, Content: "stale chunking of " + id},
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	res, err := BackfillChunks(projectDir, outputDir, 100, 0, chunkStore, vecStore, backfillEmbedder{}, db)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.Articles != 3 {
		t.Errorf("articles = %d, want 3 (concept + output + summary)", res.Articles)
	}
	if res.Sources != 1 {
		t.Errorf("sources = %d, want 1 (src: doc)", res.Sources)
	}

	all, err := chunkStore.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range all {
		seen[c.DocID] = true
		if strings.HasPrefix(c.Content, "stale chunking of ") {
			t.Errorf("%s kept its old chunk text — the rebuild wrote some other doc ID", c.DocID)
		}
	}
	for _, want := range append([]string{"concept:boundaries"}, realIDs...) {
		if !seen[want] {
			t.Errorf("%s has no chunks after the rebuild", want)
		}
	}
	// Phantom IDs a filename-derived convention would have produced.
	for _, phantom := range []string{"output:answered", "summary:sources-paper"} {
		if seen[phantom] {
			t.Errorf("rebuild created phantom doc %q — no entry row backs that ID", phantom)
		}
	}
}

// Chunk IDs change when the chunking changes, so the rebuild deletes each
// document's chunk vectors. With an embedder they come back; without one they
// are gone — which is why the CLI refuses that combination unless asked.
func TestBackfillChunkVectorLifecycle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	projectDir := t.TempDir()
	const outputDir = "wiki"
	writeLongArticle(t, projectDir, outputDir, "boundaries")

	chunkStore := memory.NewChunkStore(db)
	vecStore := vectors.NewStore(db)

	if _, err := BackfillChunks(projectDir, outputDir, 100, 0, chunkStore, vecStore, backfillEmbedder{}, db); err != nil {
		t.Fatalf("backfill with embedder: %v", err)
	}
	has, err := vecStore.HasChunkVectors("concept:boundaries")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("rebuild with an embedder left no chunk vectors")
	}

	if _, err := BackfillChunks(projectDir, outputDir, 100, 0, chunkStore, vecStore, nil, db); err != nil {
		t.Fatalf("backfill without embedder: %v", err)
	}
	has, err = vecStore.HasChunkVectors("concept:boundaries")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("chunk vectors survived a nil-embedder rebuild — the CLI guard's premise is wrong")
	}
}
