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

// V-M1a end-to-end: runSearch must apply the CONFIGURED hybrid weights at
// its call site. With hybrid_weight_bm25: 2.0 a rank-1 BM25-only hit fuses
// to 2.0/(60+1) ≈ 0.0328; if runSearch regressed to zero-weight opts the
// hybrid default 1.0 would yield 0.0164 — the assertion distinguishes them.
func TestRunSearchAppliesConfigWeights(t *testing.T) {
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Raise the BM25 weight in the generated config.
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "hybrid_weight_bm25: 0.7", "hybrid_weight_bm25: 2.0", 1)
	if edited == string(raw) {
		t.Fatal("fixture drift: hybrid_weight_bm25 not found in greenfield config")
	}
	if err := os.WriteFile(cfgPath, []byte(edited), 0644); err != nil {
		t.Fatal(err)
	}

	// Index one entry the query will BM25-match at rank 1.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	memory.NewStore(db).Add(memory.Entry{
		ID:          "concept:flux",
		Content:     "quantum flux capacitor alignment",
		ArticlePath: "wiki/concepts/flux.md",
	})
	db.Close()

	// Run the real command path with globals swapped in.
	oldDir, oldFormat := projectDir, outputFormat
	projectDir, outputFormat = dir, "json"
	defer func() { projectDir, outputFormat = oldDir, oldFormat }()

	rOut, wOut, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = wOut
	err = runSearch(searchCmd, []string{"quantum", "flux"})
	wOut.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	out, _ := io.ReadAll(rOut)

	var payload struct {
		Ok   bool `json:"ok"`
		Data []struct {
			ID       string  `json:"ID"`
			RRFScore float64 `json:"RRFScore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("parse output %q: %v", out, err)
	}
	if len(payload.Data) == 0 {
		t.Fatal("expected a search hit")
	}
	got := payload.Data[0].RRFScore
	want := 2.0 / 61.0 // configured weight 2.0, rank 1
	if got < want-1e-9 || got > want+1e-9 {
		t.Errorf("RRFScore = %v, want %v — configured BM25 weight not applied at the runSearch call site (default 1.0 would give %v)",
			got, want, 1.0/61.0)
	}
}
