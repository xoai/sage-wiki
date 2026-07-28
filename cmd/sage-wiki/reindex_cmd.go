package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/vectors"
)

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Rebuild the chunk index from articles on disk",
	Long: "Re-chunks every compiled article with the CURRENT search.chunk_size and\n" +
		"search.chunk_overlap_tokens, replacing chunk FTS rows and chunk vectors.\n" +
		"No LLM article writing happens — only chunking and (unless --no-embed)\n" +
		"chunk embeddings.\n\n" +
		"Chunking config takes effect only here: changing search.chunk_overlap_tokens\n" +
		"and then compiling normally would leave old articles at their old chunking\n" +
		"and mix the two. Change the value and run reindex as one step.",
	RunE: runReindex,
}

func runReindex(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	noEmbed, _ := cmd.Flags().GetBool("no-embed")

	// An explicit reindex must not silently re-chunk at defaults when the
	// config it is meant to apply failed to load (same reasoning as F-043).
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	db, err := storedial.OpenConcrete(dir, cfg.Storage)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	chunkStore := memory.NewChunkStore(db)
	vecStore := vectors.NewStore(db, vectors.WithANN(cfg.Search.ANNEnabled()))

	var embedder embed.Embedder
	if !noEmbed {
		embedder = embed.NewFromConfig(cfg)
	}

	before, _ := chunkStore.Count()
	if err := compiler.BackfillChunks(dir, cfg.Output, cfg.Search.ChunkSizeOrDefault(),
		cfg.Search.ChunkOverlapOrDefault(), chunkStore, vecStore, embedder, db); err != nil {
		return cli.CLIError(outputFormat, err)
	}
	after, _ := chunkStore.Count()

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]any{
			"chunks_before": before,
			"chunks_after":  after,
			"chunk_size":    cfg.Search.ChunkSizeOrDefault(),
			"chunk_overlap": cfg.Search.ChunkOverlapOrDefault(),
			"embeddings":    !noEmbed,
		}, ""))
		return nil
	}

	fmt.Printf("Reindexed chunks: %d → %d (chunk_size %d, overlap %d)\n",
		before, after, cfg.Search.ChunkSizeOrDefault(), cfg.Search.ChunkOverlapOrDefault())
	if noEmbed {
		fmt.Fprintln(os.Stderr, "note: --no-embed — chunk vectors were not regenerated; vector search over chunks stays stale until the next embed pass")
	}
	return nil
}
