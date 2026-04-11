package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xoai/sage-wiki/internal/hub"
	"github.com/xoai/sage-wiki/internal/linter"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestIntegrationM1 is an end-to-end test for Milestone 1:
// init → populate → search → ontology query → status → MCP read tools
func TestIntegrationM1(t *testing.T) {
	dir := t.TempDir()

	// Step 1: Initialize greenfield project
	t.Run("init", func(t *testing.T) {
		if err := wiki.InitGreenfield(dir, "integration-test", "gemini-2.5-flash"); err != nil {
			t.Fatalf("init: %v", err)
		}

		// Verify structure
		for _, path := range []string{"raw", "wiki/concepts", ".sage", "config.yaml", ".manifest.json"} {
			if _, err := os.Stat(filepath.Join(dir, path)); os.IsNotExist(err) {
				t.Fatalf("expected %s to exist", path)
			}
		}
	})

	// Step 2: Create MCP server (opens DB, registers tools)
	srv, err := mcppkg.NewServer(dir)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Step 3: Populate data via internal stores
	t.Run("populate", func(t *testing.T) {
		// Add FTS5 entries
		entries := []memory.Entry{
			{ID: "self-attention", Content: "Self-attention computes contextual representations by relating different positions", Tags: []string{"concept", "attention"}, ArticlePath: "wiki/concepts/self-attention.md"},
			{ID: "flash-attention", Content: "Flash attention optimizes memory access patterns for attention computation", Tags: []string{"technique", "attention", "optimization"}, ArticlePath: "wiki/concepts/flash-attention.md"},
			{ID: "kv-cache", Content: "Key-value cache stores previously computed attention keys and values for autoregressive inference", Tags: []string{"concept", "inference"}, ArticlePath: "wiki/concepts/kv-cache.md"},
		}
		for _, e := range entries {
			if err := srv.MemStore().Add(e); err != nil {
				t.Fatalf("add entry %s: %v", e.ID, err)
			}
		}

		// Add vector embeddings
		srv.VecStore().Upsert("self-attention", []float32{0.9, 0.1, 0.0, 0.0})
		srv.VecStore().Upsert("flash-attention", []float32{0.7, 0.3, 0.0, 0.0})
		srv.VecStore().Upsert("kv-cache", []float32{0.5, 0.1, 0.4, 0.0})

		// Add ontology entities
		srv.OntStore().AddEntity(ontology.Entity{ID: "self-attention", Type: "concept", Name: "Self-Attention"})
		srv.OntStore().AddEntity(ontology.Entity{ID: "flash-attention", Type: "technique", Name: "Flash Attention"})
		srv.OntStore().AddEntity(ontology.Entity{ID: "kv-cache", Type: "concept", Name: "KV Cache"})

		// Add relations
		srv.OntStore().AddRelation(ontology.Relation{ID: "r1", SourceID: "flash-attention", TargetID: "self-attention", Relation: "implements"})
		srv.OntStore().AddRelation(ontology.Relation{ID: "r2", SourceID: "kv-cache", TargetID: "self-attention", Relation: "optimizes"})

		// Write a test article file
		os.WriteFile(filepath.Join(dir, "wiki", "concepts", "self-attention.md"), []byte(`---
concept: self-attention
aliases: [scaled dot-product attention]
confidence: high
---

# Self-Attention

Self-attention computes contextual representations.
`), 0644)
	})

	// Step 4: Search via MCP
	t.Run("search_bm25", func(t *testing.T) {
		result := callTool(t, srv, "wiki_search", map[string]any{
			"query": "attention optimization",
			"limit": float64(5),
		})

		var results []map[string]any
		json.Unmarshal([]byte(result), &results)
		if len(results) == 0 {
			t.Fatal("expected search results")
		}
		// flash-attention should rank high (matches both terms)
		found := false
		for _, r := range results {
			if r["ID"] == "flash-attention" {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected flash-attention in results")
		}
	})

	// Step 5: Search with tag filter
	t.Run("search_tag_filter", func(t *testing.T) {
		result := callTool(t, srv, "wiki_search", map[string]any{
			"query": "attention",
			"tags":  "optimization",
		})

		var results []map[string]any
		json.Unmarshal([]byte(result), &results)
		if len(results) != 1 {
			t.Fatalf("expected 1 result with optimization tag, got %d", len(results))
		}
		if results[0]["ID"] != "flash-attention" {
			t.Errorf("expected flash-attention, got %s", results[0]["ID"])
		}
	})

	// Step 6: Read article via MCP
	t.Run("read_article", func(t *testing.T) {
		result := callTool(t, srv, "wiki_read", map[string]any{
			"path": "wiki/concepts/self-attention.md",
		})
		if result == "" {
			t.Fatal("expected article content")
		}
		if !contains(result, "Self-Attention") {
			t.Error("expected article to contain 'Self-Attention'")
		}
	})

	// Step 7: Ontology query via MCP
	t.Run("ontology_query", func(t *testing.T) {
		result := callTool(t, srv, "wiki_ontology_query", map[string]any{
			"entity":    "self-attention",
			"direction": "inbound",
			"depth":     float64(1),
		})

		var entities []map[string]any
		json.Unmarshal([]byte(result), &entities)
		if len(entities) != 2 {
			t.Fatalf("expected 2 inbound entities (flash-attention, kv-cache), got %d", len(entities))
		}
	})

	// Step 8: Status via MCP
	t.Run("status", func(t *testing.T) {
		result := callTool(t, srv, "wiki_status", nil)
		if !contains(result, "integration-test") {
			t.Error("expected project name in status")
		}
		if !contains(result, "Entries: 3") {
			t.Errorf("expected 3 entries in status, got: %s", result)
		}
	})

	// Step 9: List via MCP
	t.Run("list_concepts", func(t *testing.T) {
		result := callTool(t, srv, "wiki_list", map[string]any{
			"type": "concept",
		})

		var listResult map[string]any
		json.Unmarshal([]byte(result), &listResult)
		entities := listResult["entities"].([]any)
		if len(entities) != 2 {
			t.Errorf("expected 2 concepts, got %d", len(entities))
		}
	})

	// Step 10: Verify vector count
	t.Run("vectors", func(t *testing.T) {
		count, _ := srv.VecStore().Count()
		if count != 3 {
			t.Errorf("expected 3 vectors, got %d", count)
		}
		dims, _ := srv.VecStore().Dimensions()
		if dims != 4 {
			t.Errorf("expected 4 dimensions, got %d", dims)
		}
	})

	// Step 11: Lint (no API needed)
	t.Run("lint", func(t *testing.T) {
		runner := linter.NewRunner()
		ctx := &linter.LintContext{
			ProjectDir:     dir,
			OutputDir:      "wiki",
			DBPath:         filepath.Join(dir, ".sage", "wiki.db"),
			ValidRelations: []string{"implements", "optimizes"},
		}
		results, err := runner.Run(ctx, "", false)
		if err != nil {
			t.Fatalf("lint: %v", err)
		}
		t.Logf("lint findings: %d", len(results))
	})

	// Step 12: Hub add + list (no API needed)
	t.Run("hub_add_list", func(t *testing.T) {
		hubCfg := hub.New()
		overwritten := hubCfg.AddProject("test-project", hub.Project{
			Path: dir, Searchable: true,
		})
		if overwritten {
			t.Error("first add should return false")
		}
		overwritten = hubCfg.AddProject("test-project", hub.Project{
			Path: dir, Searchable: true, Description: "updated",
		})
		if !overwritten {
			t.Error("second add should return true (overwrite)")
		}
		projects := hubCfg.SearchableProjects()
		if len(projects) != 1 {
			t.Errorf("expected 1 searchable project, got %d", len(projects))
		}
	})

	// Step 13: Learn via StoreLearning (no API needed)
	t.Run("learn", func(t *testing.T) {
		db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
		if err != nil {
			t.Fatalf("open db: %v", err)
		}
		defer db.Close()
		err = linter.StoreLearning(db, "gotcha", "integration test learning", "test", "integration")
		if err != nil {
			t.Fatalf("StoreLearning: %v", err)
		}
	})

	// Step 14: Ontology list (no API needed)
	t.Run("ontology_list_entities", func(t *testing.T) {
		entities, err := srv.OntStore().ListEntities("concept")
		if err != nil {
			t.Fatalf("ListEntities: %v", err)
		}
		if len(entities) != 2 {
			t.Errorf("expected 2 concept entities, got %d", len(entities))
		}
	})

	// Step 15: Ontology list relations (no API needed)
	t.Run("ontology_list_relations", func(t *testing.T) {
		rels, err := srv.OntStore().ListRelations("", 100)
		if err != nil {
			t.Fatalf("ListRelations: %v", err)
		}
		if len(rels) != 2 {
			t.Errorf("expected 2 relations, got %d", len(rels))
		}
	})
}

// callTool invokes an MCP tool handler and returns the text result.
func callTool(t *testing.T, srv *mcppkg.Server, tool string, args map[string]any) string {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}

	// Use the exported handler map
	req := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      tool,
			Arguments: args,
		},
	}

	result := srv.CallTool(context.Background(), tool, req)
	if result == nil {
		t.Fatalf("tool %s returned nil", tool)
	}
	if result.IsError {
		t.Fatalf("tool %s error: %s", tool, result.Content[0].(mcp.TextContent).Text)
	}

	return result.Content[0].(mcp.TextContent).Text
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
