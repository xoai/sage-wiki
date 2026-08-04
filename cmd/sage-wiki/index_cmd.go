package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/engine"
)

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Manage derived search indexes",
}

var rebuildVectorsCmd = &cobra.Command{
	Use:   "rebuild-vectors",
	Short: "Rebuild the on-disk vector index from the stored embeddings",
	Long: "Regenerates .sage/vectors.idx and .sage/vectors-chunks.idx from the\n" +
		"embeddings already persisted in SQLite (embeddings are paid-for artifacts;\n" +
		"the index files are derived caches). Required once after enabling\n" +
		"vectors.backend: mmap, and again after any compile/re-embed — a stale\n" +
		"snapshot falls back to the in-memory cache with a warning, never to\n" +
		"wrong results.\n\n" +
		"Quantization: fp32 (default) is exact — results identical to the\n" +
		"in-memory backend. int8 is 4x smaller with a measured recall trade-off\n" +
		"(recall@10 >= 0.95 on the reference fixture). The bounded-memory\n" +
		"ceiling is unix-only; other platforms serve the index from memory.",
	RunE: runIndexRebuildVectors,
}

func runIndexRebuildVectors(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	quantize, _ := cmd.Flags().GetString("quantize")
	upgrade, _ := cmd.Flags().GetBool("upgrade")

	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return fmt.Errorf("index rebuild-vectors: %w", err)
	}
	quantName := quantize
	if quantName == "" {
		quantName = cfg.VectorQuantization()
	}
	quant := vectors.QuantNone
	switch quantName {
	case "none":
	case "int8":
		quant = vectors.QuantInt8
	default:
		return fmt.Errorf("index rebuild-vectors: invalid quantization %q (valid: none, int8)", quantName)
	}

	openOpts := []engine.Option{}
	if upgrade {
		openOpts = append(openOpts, engine.WithUpgrade())
	}
	evOpts, evClose := cliEventPlane(cmd.Context(), dir)
	defer evClose()
	openOpts = append(openOpts, evOpts...)
	w, err := engine.Open(cmd.Context(), dir, openOpts...)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer w.Close()
	if w.RequiresUpgrade() {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"workspace predates format versioning — re-run with --upgrade to adopt it before rebuilding indexes"))
	}

	db, err := storedial.OpenConcrete(dir, cfg.Storage)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	sageDir := filepath.Join(dir, ".sage")
	for _, job := range []struct {
		name  string
		table vectors.IndexTable
		file  string
	}{
		{"documents", vectors.IndexTableDocs, "vectors.idx"},
		{"chunks", vectors.IndexTableChunks, "vectors-chunks.idx"},
	} {
		start := time.Now()
		stats, err := vectors.WriteIndexFile(db, job.table, filepath.Join(sageDir, job.file), quant)
		if err != nil {
			return cli.CLIError(outputFormat, fmt.Errorf("rebuilding %s index: %w", job.name, err))
		}
		fmt.Fprintf(os.Stdout, "%s: %d rows, dim %d, %d bytes, quantization %s, %s",
			job.name, stats.Count, stats.Dim, stats.Bytes, quantName, time.Since(start).Round(time.Millisecond))
		if stats.Skipped > 0 {
			fmt.Fprintf(os.Stdout, " (%d dimension-mismatch rows skipped)", stats.Skipped)
		}
		fmt.Fprintln(os.Stdout)
	}
	return nil
}
