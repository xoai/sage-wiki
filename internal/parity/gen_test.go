package parity

import (
	"encoding/json"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"os"
	"path/filepath"
	"testing"

	mcppkg "github.com/xoai/sage-wiki/internal/mcp"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"context"
)

// TestRecordFixtures records LLM fixtures via the scripted origin (or
// ORIGIN for a real vendor). Guarded: requires SAGE_PARITY_FORCE=1.
// Maintainer action only — CI never runs this.
func TestRecordFixtures(t *testing.T) {
	if os.Getenv("SAGE_PARITY_FORCE") != "1" {
		t.Skip("record-fixtures requires SAGE_PARITY_FORCE=1")
	}
	originURL := os.Getenv("ORIGIN")
	apiKey, model := "sk-replay", "gpt-4o-mini"
	if originURL == "" {
		origin := NewOriginServer()
		defer origin.Close()
		originURL = origin.URL
	} else {
		// Real-vendor record: KEY and MODEL are required and used.
		apiKey = os.Getenv("KEY")
		if apiKey == "" {
			t.Fatal("real-vendor record requires KEY=<api key>")
		}
		if m := os.Getenv("MODEL"); m != "" {
			model = m
		}
	}
	fixtureDir := filepath.Join("..", "..", "testdata", "fixtures", "openai")
	// Clear stale fixtures: a new record run replaces the fixture set
	// wholesale (orphans from older prompts must not linger).
	if err := os.RemoveAll(fixtureDir); err != nil {
		t.Fatal(err)
	}
	rec, err := NewRecordServer(originURL, fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.Close()

	corpus := filepath.Join("..", "..", "testdata", "golden-corpus")
	ws := filepath.Join(t.TempDir(), "ws")
	goldenCfg := readGoldenConfig(t)
	if err := BuildWorkspaceAuth(corpus, ws, rec.URL(), goldenCfg, apiKey, model); err != nil {
		t.Fatalf("record build: %v", err)
	}

	// Warm the query fixtures: record /embeddings for every golden query
	// text so the serve-mode REST path (which embeds via config, not the
	// in-process fnvEmbedder) replays them too (AC-S2).
	searchGoldenPath := filepath.Join("..", "..", "testdata", "golden", "search.json")
	raw, err := os.ReadFile(searchGoldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var sg SearchGolden
	if err := json.Unmarshal(raw, &sg); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(ws, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	emb := embed.NewFromConfig(cfg)
	for _, q := range sg.Queries {
		if _, err := emb.Embed(q.Q); err != nil {
			t.Fatalf("record query embedding %q: %v", q.Q, err)
		}
	}

	// Warm the graph-QA fixture for the MCP streamable test (AC-S3):
	// wiki_graph_query synthesizes via LLM — record that prompt too.
	mcpSrv, err := mcppkg.NewServer(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpSrv.Close()
	res := mcpSrv.CallTool(context.Background(), "wiki_graph_query", mcpgo.CallToolRequest{
		Params: mcpgo.CallToolParams{
			Name:      "wiki_graph_query",
			Arguments: map[string]any{"question": "api gateway", "mode": "local"},
		},
	})
	if res != nil && res.IsError {
		t.Fatalf("warm graph QA fixture: %+v", res.Content)
	}

	entries, _ := os.ReadDir(fixtureDir)
	t.Logf("recorded %d fixtures into %s", len(entries), fixtureDir)
}

// TestRegenGoldens rebuilds against the replay server and rewrites all
// goldens. Guarded: requires SAGE_PARITY_FORCE=1.
func TestRegenGoldens(t *testing.T) {
	if os.Getenv("SAGE_PARITY_FORCE") != "1" {
		t.Skip("regen-goldens requires SAGE_PARITY_FORCE=1")
	}
	fixtureDir := filepath.Join("..", "..", "testdata", "fixtures", "openai")
	replay, err := NewReplayServer(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()

	corpus := filepath.Join("..", "..", "testdata", "golden-corpus")
	ws := filepath.Join(t.TempDir(), "ws")
	goldenCfg := readGoldenConfig(t)
	if err := BuildWorkspace(corpus, ws, replay.URL(), goldenCfg); err != nil {
		t.Fatalf("regen build: %v", err)
	}
	goldenDir := filepath.Join("..", "..", "testdata", "golden")
	goldenConfigPath := filepath.Join(goldenDir, "config.yaml")
	if err := RegenGoldens(ws, goldenConfigPath, goldenDir); err != nil {
		t.Fatalf("regen: %v", err)
	}

	// Search golden: expectations captured from the built workspace.
	searchGoldenPath := filepath.Join(goldenDir, "search.json")
	raw, err := os.ReadFile(searchGoldenPath)
	if err != nil {
		t.Fatalf("search.json must be hand-authored first: %v", err)
	}
	var sg SearchGolden
	if err := json.Unmarshal(raw, &sg); err != nil {
		t.Fatal(err)
	}
	results, err := RunSearchSet(ws, sg.Queries)
	if err != nil {
		t.Fatal(err)
	}
	for i := range sg.Queries {
		sg.Queries[i].Expect = results[i]
	}
	sg.GoldenFormatVersion = 1
	out, err := json.MarshalIndent(sg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(searchGoldenPath, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("goldens regenerated into %s", goldenDir)
}

func readGoldenConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
