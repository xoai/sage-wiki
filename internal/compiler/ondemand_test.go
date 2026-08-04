package compiler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/events"
)

// TestCompileTopic_FilterAndPromote verifies the on-demand flow:
// 1. Sources indexed at Tier 1 are searchable via FTS5
// 2. CompileTopic finds them via search
// 3. Uncompiled sources are promoted to Tier 3
// 4. Already-compiled sources are skipped
//
// The full pipeline (LLM calls) is NOT tested here — that requires
// integration_test.go with real LLM mocking. This tests the search→filter→promote logic.
func TestCompileTopic_FilterAndPromote(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	items := NewCompileItemStore(db, config.NowUTC)

	// Create source files on disk
	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)

	// Source 1: Tier 1, uncompiled — should be found and promoted
	os.WriteFile(filepath.Join(projectDir, "raw/attention.md"),
		[]byte("# Flash Attention\n\nFlash attention optimizes memory access patterns for transformer models."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/attention.md", Hash: "aaa", FileType: "md",
		Tier: 1, SourceType: "compiler",
	})
	// Index it in FTS5 so search finds it
	memStore.Add(memory.Entry{
		ID:      "src:raw/attention.md",
		Content: "Flash attention optimizes memory access patterns for transformer models.",
		Tags:    []string{"md", "tier:1"},
	})

	// Source 2: Tier 3, already compiled — should be skipped
	os.WriteFile(filepath.Join(projectDir, "raw/transformers.md"),
		[]byte("# Transformers\n\nSelf-attention is the key mechanism."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/transformers.md", Hash: "bbb", FileType: "md",
		Tier: 3, SourceType: "compiler", PassWritten: true,
	})
	memStore.Add(memory.Entry{
		ID:      "raw/transformers.md", // compiled entry (no src: prefix)
		Content: "Transformers use self-attention mechanism.",
		Tags:    []string{"md"},
	})

	// Source 3: Tier 0, uncompiled, irrelevant topic — should NOT match
	os.WriteFile(filepath.Join(projectDir, "raw/databases.md"),
		[]byte("# SQLite\n\nSQLite uses WAL mode for concurrent access."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/databases.md", Hash: "ccc", FileType: "md",
		Tier: 0, SourceType: "compiler",
	})
	memStore.Add(memory.Entry{
		ID:      "src:raw/databases.md",
		Content: "SQLite uses WAL mode for concurrent access.",
		Tags:    []string{"md", "tier:0"},
	})

	searcher := hybrid.NewSearcher(memStore, vecStore)

	cfg := &config.Config{
		Compiler: config.CompilerConfig{
			MaxParallel: 4,
		},
	}

	// Stub LLM server — returns 500 instantly so the pipeline fails fast
	// without waiting for TCP timeouts.
	stubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stub: no real LLM"}}`, http.StatusUnauthorized)
	}))
	defer stubServer.Close()

	stubClient, err := llm.NewClient("openai", "fake-key", stubServer.URL, 100)
	if err != nil {
		t.Fatalf("create stub client: %v", err)
	}

	result, err := CompileTopic(context.Background(), OnDemandOpts{
		Topic:      "attention transformer",
		MaxSources: 10,
		ProjectDir: projectDir,
		Config:     cfg,
		DB:         db,
		Searcher:   searcher,
		Embedder:   nil, // BM25 only
		Client:     stubClient,
	})
	// Pipeline will error (no reachable LLM endpoint), but promotion
	// should have persisted in SQLite before the pipeline ran.
	if err != nil {
		t.Logf("Expected pipeline error (stub LLM): %v", err)
	}

	// Verify: attention.md should have been promoted to Tier 3
	attItem, _ := items.GetByPath("raw/attention.md")
	if attItem == nil {
		t.Fatal("raw/attention.md should exist in compile_items")
	}
	if attItem.Tier != 3 {
		t.Errorf("raw/attention.md tier = %d, want 3 (should be promoted)", attItem.Tier)
	}

	// Verify: transformers.md should still be Tier 3 (was already compiled)
	txItem, _ := items.GetByPath("raw/transformers.md")
	if txItem == nil {
		t.Fatal("raw/transformers.md should exist")
	}
	if txItem.Tier != 3 {
		t.Errorf("raw/transformers.md tier = %d, want 3", txItem.Tier)
	}

	// Verify: databases.md should NOT have been promoted (irrelevant topic)
	dbItem, _ := items.GetByPath("raw/databases.md")
	if dbItem == nil {
		t.Fatal("raw/databases.md should exist")
	}
	if dbItem.Tier != 0 {
		t.Errorf("raw/databases.md tier = %d, want 0 (irrelevant topic, should not promote)", dbItem.Tier)
	}

	// Log result if available
	if result != nil {
		// The stub client makes the pipeline fail, but CompileTopic should
		// have set CompiledSources before entering the pipeline.
		if result.CompiledSources != 1 {
			t.Errorf("compiled sources = %d, want 1 (attention.md)", result.CompiledSources)
		}
		t.Logf("CompileTopic result: sources=%d articles=%d msg=%q",
			result.CompiledSources, result.ArticlesWritten, result.Message)
	}
}

// TestCompileTopic_SkipsCompiledSources verifies that CompileTopic filters
// out already-compiled sources INDEPENDENTLY of FTS5 ranking. Both sources
// contain the same topic keywords and will be returned by search, but only
// the uncompiled one should be promoted. This tests the filter logic in
// CompileTopic (lines 83-112 of ondemand.go), not the search engine.
func TestCompileTopic_SkipsCompiledSources(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	items := NewCompileItemStore(db, config.NowUTC)

	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)

	// Both sources contain "flash attention" — both WILL be returned by search.
	// Source A: Tier 1, uncompiled — should be promoted
	os.WriteFile(filepath.Join(projectDir, "raw/flash-v1.md"),
		[]byte("# Flash Attention v1\n\nFlash attention reduces memory usage."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/flash-v1.md", Hash: "v1", FileType: "md",
		Tier: 1, SourceType: "compiler",
	})
	memStore.Add(memory.Entry{
		ID:      "src:raw/flash-v1.md",
		Content: "Flash attention v1 reduces memory usage in transformers.",
		Tags:    []string{"md", "tier:1"},
	})

	// Source B: Tier 3, already compiled — ALSO matches search, but should be SKIPPED
	os.WriteFile(filepath.Join(projectDir, "raw/flash-v2.md"),
		[]byte("# Flash Attention v2\n\nFlash attention v2 improves on v1."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/flash-v2.md", Hash: "v2", FileType: "md",
		Tier: 3, SourceType: "compiler", PassWritten: true,
	})
	memStore.Add(memory.Entry{
		ID:      "raw/flash-v2.md", // compiled entry
		Content: "Flash attention v2 improves on v1 with better memory access.",
		Tags:    []string{"md"},
	})

	searcher := hybrid.NewSearcher(memStore, vecStore)
	cfg := &config.Config{
		Compiler: config.CompilerConfig{MaxParallel: 4},
	}

	stubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stub"}}`, http.StatusUnauthorized)
	}))
	defer stubServer.Close()

	stubClient, err := llm.NewClient("openai", "fake-key", stubServer.URL, 100)
	if err != nil {
		t.Fatalf("create stub client: %v", err)
	}

	result, err := CompileTopic(context.Background(), OnDemandOpts{
		Topic:      "flash attention",
		MaxSources: 10,
		ProjectDir: projectDir,
		Config:     cfg,
		DB:         db,
		Searcher:   searcher,
		Client:     stubClient,
	})
	if err != nil {
		t.Logf("Expected pipeline error: %v", err)
	}

	// flash-v1.md (uncompiled) should be promoted to Tier 3
	v1, _ := items.GetByPath("raw/flash-v1.md")
	if v1 == nil {
		t.Fatal("flash-v1.md should exist")
	}
	if v1.Tier != 3 {
		t.Errorf("flash-v1.md tier = %d, want 3 (uncompiled, should promote)", v1.Tier)
	}

	// flash-v2.md (already compiled) should stay at Tier 3 with PassWritten unchanged
	v2, _ := items.GetByPath("raw/flash-v2.md")
	if v2 == nil {
		t.Fatal("flash-v2.md should exist")
	}
	if v2.Tier != 3 {
		t.Errorf("flash-v2.md tier = %d, want 3", v2.Tier)
	}

	// Only 1 source should have been selected for compilation (flash-v1.md)
	if result != nil && result.CompiledSources != 1 {
		t.Errorf("compiled sources = %d, want 1 (only flash-v1.md)", result.CompiledSources)
	}
}

func TestCompileTopic_AllCompiled(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	items := NewCompileItemStore(db, config.NowUTC)

	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)

	// Only compiled sources exist
	os.WriteFile(filepath.Join(projectDir, "raw/done.md"),
		[]byte("# Done\n\nAlready compiled content."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/done.md", Hash: "xxx", FileType: "md",
		Tier: 3, SourceType: "compiler", PassWritten: true,
	})
	memStore.Add(memory.Entry{
		ID:      "raw/done.md",
		Content: "Already compiled content.",
		Tags:    []string{"md"},
	})

	searcher := hybrid.NewSearcher(memStore, vecStore)
	cfg := &config.Config{
		Compiler: config.CompilerConfig{MaxParallel: 4},
	}

	result, err := CompileTopic(context.Background(), OnDemandOpts{
		Topic:      "compiled content",
		MaxSources: 10,
		ProjectDir: projectDir,
		Config:     cfg,
		DB:         db,
		Searcher:   searcher,
	})

	if err != nil {
		t.Fatalf("CompileTopic with all compiled: %v", err)
	}
	if result.CompiledSources != 0 {
		t.Errorf("compiled sources = %d, want 0", result.CompiledSources)
	}
	if result.Message == "" {
		t.Error("expected status message for all-compiled case")
	}
}

// TestCompileTopic_EmitsPromotionEvents (SPEC-07 §4 + QA F-003): an
// on-demand compile emits promotion_triggered{trigger:compile-on-demand}
// through OnDemandOpts.Sink with the true from/to tiers.
func TestCompileTopic_EmitsPromotionEvents(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	items := NewCompileItemStore(db, config.NowUTC)

	projectDir := t.TempDir()
	os.MkdirAll(filepath.Join(projectDir, "raw"), 0755)
	os.WriteFile(filepath.Join(projectDir, "raw/attention.md"),
		[]byte("# Flash Attention\n\nFlash attention optimizes memory access patterns for transformer models."), 0644)
	items.Upsert(CompileItem{
		SourcePath: "raw/attention.md", Hash: "aaa", FileType: "md",
		Tier: 1, SourceType: "compiler",
	})
	memStore.Add(memory.Entry{
		ID:      "src:raw/attention.md",
		Content: "Flash attention optimizes memory access patterns for transformer models.",
		Tags:    []string{"md", "tier:1"},
	})

	searcher := hybrid.NewSearcher(memStore, vecStore)
	cfg := &config.Config{Compiler: config.CompilerConfig{MaxParallel: 4}}

	stubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"stub: no real LLM"}}`, http.StatusUnauthorized)
	}))
	defer stubServer.Close()
	stubClient, err := llm.NewClient("openai", "fake-key", stubServer.URL, 100)
	if err != nil {
		t.Fatal(err)
	}

	sink := &ondemandCaptureSink{}
	_, _ = CompileTopic(context.Background(), OnDemandOpts{ // pipeline errors (stub LLM); promotions emit before it
		Topic:      "attention transformer",
		MaxSources: 10,
		ProjectDir: projectDir,
		Config:     cfg,
		DB:         db,
		Searcher:   searcher,
		Embedder:   nil,
		Client:     stubClient,
		Sink:       sink,
	})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var found *events.PromotionTriggered
	for _, ev := range sink.events {
		if ev.Type == events.TypePromotionTriggered {
			d := ev.Data.(events.PromotionTriggered)
			found = &d
		}
	}
	if found == nil {
		t.Fatalf("no promotion_triggered event reached the sink (events: %d)", len(sink.events))
	}
	if found.DocID != "raw/attention.md" || found.FromTier != 1 || found.ToTier != 3 || found.Trigger != "compile-on-demand" {
		t.Errorf("payload = %+v, want raw/attention.md 1→3 compile-on-demand", found)
	}
}

type ondemandCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *ondemandCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}
