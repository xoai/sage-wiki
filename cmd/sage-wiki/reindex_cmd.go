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
	Short: "Rebuild the chunk index from the documents on disk",
	Long: "Re-chunks every compiled article (concepts/, summaries/, outputs/) and\n" +
		"every chunk-indexed raw source with the CURRENT search.chunk_size and\n" +
		"search.chunk_overlap_tokens, replacing chunk FTS rows and chunk vectors.\n" +
		"No LLM article writing happens — only chunking and chunk embeddings.\n\n" +
		"Chunking config takes effect only here: changing search.chunk_overlap_tokens\n" +
		"and then compiling normally would leave unchanged documents at their old\n" +
		"chunking and mix the two. Change the value and run reindex as one step.\n\n" +
		"Re-chunking changes chunk IDs, so the old chunk vectors cannot be kept.\n" +
		"Without a working embedding provider the rebuild would leave the chunk\n" +
		"vectors empty, so it stops instead; --drop-chunk-vectors rebuilds the text\n" +
		"index anyway and leaves chunk-level vector search dead until the next\n" +
		"`sage-wiki compile --re-embed`.",
	RunE: runReindex,
}

// reindexEmbedder decides whether a rebuild may proceed. Re-chunking replaces
// chunk IDs, so DeleteDocChunks drops each document's chunk vectors on the way
// through; with no embedder there is nothing to put back. Refuse rather than
// silently empty the vector leg — the user has to ask for that outcome.
func reindexEmbedder(embedder embed.Embedder, dropVectors bool) (embed.Embedder, error) {
	if dropVectors {
		return nil, nil
	}
	if embedder == nil {
		return nil, fmt.Errorf("reindex: no embedding provider available — rebuilding now would drop every chunk vector " +
			"with nothing to replace them. Configure an embedding provider, or pass --drop-chunk-vectors to rebuild " +
			"the text index anyway (chunk-level vector search stays dead until `sage-wiki compile --re-embed`)")
	}
	return embedder, nil
}

func runReindex(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	dropVectors, _ := cmd.Flags().GetBool("drop-chunk-vectors")

	// An explicit reindex must not silently re-chunk at defaults when the
	// config it is meant to apply failed to load (same reasoning as F-043).
	// Returned raw, not via CLIError: in JSON mode CLIError exits 0, which
	// would mask exactly the typo this check exists to catch.
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}

	db, err := storedial.OpenConcrete(dir, cfg.Storage)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	chunkStore := memory.NewChunkStore(db)
	vecStore := vectors.NewStore(db, vectors.WithANN(cfg.Search.ANNEnabled()), vectors.WithVectorBackend(cfg.VectorBackend()), vectors.WithIndexDir(filepath.Join(dir, ".sage")))

	embedder, err := reindexEmbedder(embed.NewFromConfig(cfg), dropVectors)
	if err != nil {
		return err
	}

	before, err := chunkStore.Count()
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("reading the chunk index: %w", err))
	}
	res, err := compiler.BackfillChunks(dir, cfg.Output, cfg.Search.ChunkSizeOrDefault(),
		cfg.Search.ChunkOverlapOrDefault(), chunkStore, vecStore, embedder, db)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	after, err := chunkStore.Count()
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("reading the rebuilt chunk index: %w", err))
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]any{
			"chunks_before":         before,
			"chunks_after":          after,
			"articles":              res.Articles,
			"sources":               res.Sources,
			"chunk_size":            cfg.Search.ChunkSizeOrDefault(),
			"chunk_overlap":         cfg.Search.ChunkOverlapOrDefault(),
			"chunk_vectors_dropped": embedder == nil,
		}, ""))
		return nil
	}

	fmt.Printf("Reindexed %d documents (%d articles, %d sources): chunks %d → %d (chunk_size %d, overlap %d)\n",
		res.Total(), res.Articles, res.Sources, before, after,
		cfg.Search.ChunkSizeOrDefault(), cfg.Search.ChunkOverlapOrDefault())
	if embedder == nil {
		fmt.Fprintln(os.Stderr, "warning: --drop-chunk-vectors — chunk vectors were deleted and not rebuilt; "+
			"chunk-level vector search is off until `sage-wiki compile --re-embed`")
	}
	return nil
}
