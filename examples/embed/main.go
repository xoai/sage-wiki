// examples/embed demonstrates embedding sage-wiki as a Go module
// (SPEC-01): open a workspace, capture a document, compile, and search —
// fully offline via a fake provider. Run with: go run ./examples/embed
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/xoai/sage-wiki/pkg/engine"
	"github.com/xoai/sage-wiki/pkg/provider/providerfake"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()

	// A fake provider keeps the example offline: scripted completions,
	// deterministic hash embeddings, no network.
	fake := providerfake.New("Attention lets tokens weigh context.")
	fake.Responses["self-attention"] = "Self-attention computes pairwise affinities."

	// Init creates and opens a fresh workspace in a temp dir.
	dir, err := os.MkdirTemp("", "sagewiki-embed-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	w, err := engine.Init(ctx, dir, engine.WithProvider(fake))
	if err != nil {
		return fmt.Errorf("open workspace: %w", err)
	}
	defer w.Close()

	// Capture a document. A Reader capture lands in raw/captures/ — the
	// compile diff sees it as new (a file capture via Source.Path registers
	// its hash in the manifest immediately, so it would compile only on
	// later change — the CLI's ingest semantics).
	id, err := w.Capture(ctx, engine.Source{
		Reader: strings.NewReader("# Self-Attention\n\nSelf-attention computes contextual representations by comparing each token with every other token."),
	})
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	fmt.Println("captured:", id)

	// Compile at Tier 1 (index only offline — embedding needs a configured
	// provider, so this run indexes + searches via BM25; the fake provider
	// serves the engine's search side below).
	res, err := w.Compile(ctx, engine.CompileRequest{Selector: "pending", Tier: 1})
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	fmt.Printf("compiled: +%d added, %d indexed, %d embedded\n", res.Added, res.TierIndexed, res.TierEmbedded)

	// Search the workspace (BM25 over the fresh index).
	hits, err := w.Search(ctx, engine.SearchRequest{Query: "contextual representations", Limit: 3})
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	fmt.Printf("search: %d result(s)\n", len(hits.Results))
	for i, r := range hits.Results {
		fmt.Printf("  %d. [%.4f] %s\n", i+1, r.Score, r.ArticlePath)
	}

	// Stats round out the tour (tier-1 sources index into the store without
	// entering the manifest — entry count, not source count, proves it).
	stats, err := w.Stats(ctx)
	if err != nil {
		return fmt.Errorf("stats: %w", err)
	}
	fmt.Printf("stats: %d sources, %d entries\n", stats.SourceCount, stats.EntryCount)
	return nil
}
