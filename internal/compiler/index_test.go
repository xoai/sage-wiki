package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

func TestIndexRawSources_SkipsCompiledEntry(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	items := NewCompileItemStore(db)

	// Create a temporary source file
	projectDir := t.TempDir()
	srcPath := "raw/existing.md"
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)
	os.WriteFile(filepath.Join(projectDir, srcPath), []byte("# Existing Content\n\nThis source has been compiled."), 0644)

	// Pre-add a compiled entry (non-prefixed ID = compiled article entry)
	memStore.Add(memory.Entry{
		ID:      srcPath, // no "src:" prefix — this is a compiled entry
		Content: "Compiled summary of existing content.",
		Tags:    []string{"md"},
	})

	// Create compile item at Tier 0
	items.Upsert(CompileItem{
		SourcePath: srcPath,
		Hash:       "abc123",
		FileType:   "md",
		Tier:       0,
		SourceType: "compiler",
	})

	sources := []CompileItem{
		{SourcePath: srcPath, FileType: "md"},
	}

	// indexRawSources should skip adding a "src:" entry because a compiled entry exists
	indexed := indexRawSources(projectDir, sources, memStore, items)
	if indexed != 1 {
		t.Errorf("indexed = %d, want 1 (should count as indexed even when skipped)", indexed)
	}

	// Verify that the "src:" prefixed entry was NOT added
	srcEntry, _ := memStore.Get("src:" + srcPath)
	if srcEntry != nil {
		t.Error("expected no 'src:' entry when compiled entry exists, but found one")
	}

	// Verify the original compiled entry still exists
	compiledEntry, _ := memStore.Get(srcPath)
	if compiledEntry == nil {
		t.Error("compiled entry should still exist")
	}

	// Verify pass was marked
	item, _ := items.GetByPath(srcPath)
	if item == nil {
		t.Fatal("compile item should exist")
	}
	if !item.PassIndexed {
		t.Error("pass_indexed should be true after indexRawSources")
	}
}

func TestIndexRawSources_IndexesNewSource(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	items := NewCompileItemStore(db)

	// Create a source file with no compiled entry
	projectDir := t.TempDir()
	srcPath := "raw/new.md"
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)
	os.WriteFile(filepath.Join(projectDir, srcPath), []byte("# New Content\n\nThis is new and uncompiled."), 0644)

	items.Upsert(CompileItem{
		SourcePath: srcPath,
		Hash:       "def456",
		FileType:   "md",
		Tier:       0,
		SourceType: "compiler",
	})

	sources := []CompileItem{
		{SourcePath: srcPath, FileType: "md"},
	}

	indexed := indexRawSources(projectDir, sources, memStore, items)
	if indexed != 1 {
		t.Errorf("indexed = %d, want 1", indexed)
	}

	// Verify the "src:" entry WAS added
	srcEntry, _ := memStore.Get("src:" + srcPath)
	if srcEntry == nil {
		t.Error("expected 'src:' entry for new source")
	}
}

func TestIndexAndEmbedSources_ChunkLongSource(t *testing.T) {
	dir := t.TempDir()

	// Build text with paragraph breaks so splitByParagraphs can split it
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("The quick brown fox jumps over the lazy dog. This sentence adds more tokens to ensure chunking works correctly across paragraph boundaries.")
		if i%5 == 4 {
			sb.WriteString("\n\n")
		} else {
			sb.WriteString(" ")
		}
	}
	longText := sb.String()
	srcDir := filepath.Join(dir, "raw")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "long.md"), []byte(longText), 0644)

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	chunkStore := memory.NewChunkStore(db)
	itemStore := NewCompileItemStore(db)
	embedder := &mockEmbedder{embeddings: map[string][]float32{}}

	sources := []CompileItem{{SourcePath: "raw/long.md", FileType: "article"}}

	indexed, embedded := indexAndEmbedSources(dir, sources, memStore, vecStore, embedder, itemStore, nil, chunkStore, 800, db)

	if indexed != 1 {
		t.Errorf("expected 1 indexed, got %d", indexed)
	}
	if embedded != 1 {
		t.Errorf("expected 1 embedded, got %d", embedded)
	}

	results, err := chunkStore.SearchChunks("quick brown fox", 10)
	if err != nil {
		t.Fatalf("chunk search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected chunk search results for long source")
	}
	if results[0].DocID != "src:raw/long.md" {
		t.Errorf("expected doc_id 'src:raw/long.md', got %q", results[0].DocID)
	}

	// Count chunk vectors and FTS entries
	var chunkVecCount int
	db.ReadDB().QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&chunkVecCount)
	var chunkFTSCount int
	db.ReadDB().QueryRow("SELECT COUNT(*) FROM chunks_meta WHERE doc_id = ?", "src:raw/long.md").Scan(&chunkFTSCount)
	t.Logf("chunk vectors: %d, chunk FTS entries: %d", chunkVecCount, chunkFTSCount)
	if chunkFTSCount < 2 {
		t.Errorf("expected multiple chunks for long source, got %d", chunkFTSCount)
	}
}

func TestIndexAndEmbedSources_ShortSource(t *testing.T) {
	dir := t.TempDir()

	srcDir := filepath.Join(dir, "raw")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "short.md"), []byte("Short document content."), 0644)

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	chunkStore := memory.NewChunkStore(db)
	itemStore := NewCompileItemStore(db)
	embedder := &mockEmbedder{embeddings: map[string][]float32{}}

	sources := []CompileItem{{SourcePath: "raw/short.md", FileType: "article"}}
	_, embedded := indexAndEmbedSources(dir, sources, memStore, vecStore, embedder, itemStore, nil, chunkStore, 800, db)

	if embedded != 1 {
		t.Errorf("expected 1 embedded, got %d", embedded)
	}

	vecResults, _ := vecStore.SearchChunks([]float32{0.1, 0.2, 0.3, 0.4}, 10)
	if len(vecResults) != 1 {
		t.Errorf("expected 1 chunk vector for short source, got %d", len(vecResults))
	}
}

func TestIndexAndEmbedSources_EmptyText(t *testing.T) {
	dir := t.TempDir()

	srcDir := filepath.Join(dir, "raw")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "empty.md"), []byte(""), 0644)

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	chunkStore := memory.NewChunkStore(db)
	itemStore := NewCompileItemStore(db)
	embedder := &mockEmbedder{embeddings: map[string][]float32{}}

	sources := []CompileItem{{SourcePath: "raw/empty.md", FileType: "article"}}
	_, embedded := indexAndEmbedSources(dir, sources, memStore, vecStore, embedder, itemStore, nil, chunkStore, 800, db)

	if embedded != 0 {
		t.Errorf("expected 0 embedded for empty source, got %d", embedded)
	}
}

func TestIndexAndEmbedSources_NilChunkStore(t *testing.T) {
	dir := t.TempDir()

	srcDir := filepath.Join(dir, "raw")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "doc.md"), []byte("Some content for embedding."), 0644)

	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	itemStore := NewCompileItemStore(db)
	embedder := &mockEmbedder{embeddings: map[string][]float32{}}

	sources := []CompileItem{{SourcePath: "raw/doc.md", FileType: "article"}}
	_, embedded := indexAndEmbedSources(dir, sources, memStore, vecStore, embedder, itemStore, nil, nil, 800, nil)

	if embedded != 1 {
		t.Errorf("expected 1 embedded (legacy path), got %d", embedded)
	}

	vec, err := vecStore.Get("src:raw/doc.md")
	if err != nil || vec == nil {
		t.Error("expected legacy vector in vec_entries")
	}
}
