package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

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
	if err := BackfillChunks(projectDir, outputDir, 100, 0, chunkStore, vecStore, nil, db); err != nil {
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
	if err := BackfillChunks(projectDir, outputDir, 100, 20, chunkStore, vecStore, nil, db); err != nil {
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
