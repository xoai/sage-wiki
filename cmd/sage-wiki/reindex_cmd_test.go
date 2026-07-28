package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// T5.6 end-to-end: chunk overlap is a reindex-time decision. `reindex` reads
// the CURRENT config, re-chunks the articles on disk, and replaces the chunk
// rows — so the overlap the user just configured is what lands in the index.
func TestRunReindexAppliesConfiguredOverlap(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// An article long enough to split at chunk_size 100.
	var body strings.Builder
	for i := 0; i < 30; i++ {
		body.WriteString("Retrieval quality depends on where the chunk boundary falls, ")
		body.WriteString("and a fact that straddles one is easy to lose entirely.\n\n")
	}
	conceptDir := filepath.Join(dir, "wiki", "concepts")
	if err := os.MkdirAll(conceptDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conceptDir, "boundaries.md"), []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	writeChunkCfg := func(overlap string) {
		raw, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		out := make([]string, 0, len(lines)+2)
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "chunk_size:") ||
				strings.HasPrefix(strings.TrimSpace(l), "chunk_overlap_tokens:") {
				continue
			}
			out = append(out, l)
			if strings.HasPrefix(l, "search:") {
				out = append(out, "  chunk_size: 100")
				if overlap != "" {
					out = append(out, "  chunk_overlap_tokens: "+overlap)
				}
			}
		}
		if err := os.WriteFile(cfgPath, []byte(strings.Join(out, "\n")), 0644); err != nil {
			t.Fatal(err)
		}
	}

	oldDir, oldFormat := projectDir, outputFormat
	projectDir, outputFormat = dir, "json"
	defer func() { projectDir, outputFormat = oldDir, oldFormat }()

	// Chunk text is the subject here; no embedder in the fixture.
	if err := reindexCmd.Flags().Set("drop-chunk-vectors", "true"); err != nil {
		t.Fatal(err)
	}
	defer reindexCmd.Flags().Set("drop-chunk-vectors", "false")

	runReindexJSON := func(t *testing.T) map[string]any {
		t.Helper()
		rOut, wOut, _ := os.Pipe()
		oldStdout := os.Stdout
		os.Stdout = wOut
		err := runReindex(reindexCmd, nil)
		wOut.Close()
		os.Stdout = oldStdout
		if err != nil {
			t.Fatalf("runReindex: %v", err)
		}
		out, _ := io.ReadAll(rOut)
		var payload struct {
			Ok   bool           `json:"ok"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
		if !payload.Ok {
			t.Fatalf("reindex reported failure: %s", out)
		}
		return payload.Data
	}

	chunkContents := func(t *testing.T) []string {
		t.Helper()
		db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		all, err := memory.NewChunkStore(db).ListAll()
		if err != nil {
			t.Fatal(err)
		}
		texts := make([]string, len(all))
		for i, c := range all {
			texts[i] = c.Content
		}
		return texts
	}

	// Pass 1: default overlap 0.
	writeChunkCfg("")
	if got := runReindexJSON(t)["chunk_overlap"]; got != float64(0) {
		t.Fatalf("chunk_overlap = %v, want 0", got)
	}
	before := chunkContents(t)
	if len(before) < 2 {
		t.Fatalf("indexed %d chunks, want >= 2", len(before))
	}

	// Pass 2: opt into overlap, reindex again. Chunk count is stable
	// (delete-then-insert), and later chunks carry their predecessor's tail.
	writeChunkCfg("20")
	if got := runReindexJSON(t)["chunk_overlap"]; got != float64(20) {
		t.Fatalf("chunk_overlap = %v, want 20", got)
	}
	after := chunkContents(t)
	if len(after) != len(before) {
		t.Fatalf("chunk count = %d, want %d (reindex must replace, not append)", len(after), len(before))
	}
	if after[0] != before[0] {
		t.Error("first chunk changed — it has no predecessor to overlap")
	}
	grew := false
	for i := 1; i < len(after); i++ {
		if !strings.HasSuffix(after[i], before[i]) {
			t.Fatalf("chunk %d no longer ends with its original text", i)
		}
		if len(after[i]) > len(before[i]) {
			grew = true
		}
	}
	if !grew {
		t.Error("reindex with chunk_overlap_tokens: 20 produced no overlapping chunks")
	}
}

// An explicit reindex with an unloadable config must fail rather than
// silently re-chunk everything at the defaults (F-043 reasoning).
func TestRunReindexFailsOnBadConfig(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("search: [not, a, mapping]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	oldDir, oldFormat := projectDir, outputFormat
	projectDir, outputFormat = dir, "text"
	defer func() { projectDir, outputFormat = oldDir, oldFormat }()

	if err := runReindex(reindexCmd, nil); err == nil {
		t.Fatal("runReindex succeeded with an unloadable config, want error")
	}
}

// stubEmbedder is a non-nil embedder that never dials.
type stubEmbedder struct{ v []float32 }

func (s stubEmbedder) Embed(string) ([]float32, error) { return s.v, nil }
func (s stubEmbedder) Dimensions() int                 { return len(s.v) }
func (s stubEmbedder) Name() string                    { return "stub" }

// The CRITICAL half of the reindex contract: re-chunking replaces chunk IDs,
// so the rebuild drops each document's chunk vectors on the way through. With
// no embedder to rebuild them, the command must refuse — the alternative is a
// silent, exit-0 wipe of the entire chunk-vector leg.
func TestReindexEmbedderGuard(t *testing.T) {
	fake := stubEmbedder{v: []float32{0.1, 0.2}}

	if _, err := reindexEmbedder(nil, false); err == nil {
		t.Error("no embedder and no --drop-chunk-vectors must be an error")
	} else if !strings.Contains(err.Error(), "--drop-chunk-vectors") {
		t.Errorf("error must name the escape hatch, got: %v", err)
	}

	got, err := reindexEmbedder(fake, false)
	if err != nil || got == nil {
		t.Errorf("a working embedder must proceed: %v %v", got, err)
	}

	got, err = reindexEmbedder(fake, true)
	if err != nil || got != nil {
		t.Errorf("--drop-chunk-vectors must proceed WITHOUT an embedder: %v %v", got, err)
	}

	got, err = reindexEmbedder(nil, true)
	if err != nil || got != nil {
		t.Errorf("--drop-chunk-vectors with no embedder must proceed: %v %v", got, err)
	}
}
