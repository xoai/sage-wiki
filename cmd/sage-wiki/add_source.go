package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/pathsafe"
)

var addSourceCmd = &cobra.Command{
	Use:   "add-source [path]",
	Short: "Register a source file in the manifest",
	Args:  cobra.ExactArgs(1),
	RunE:  runAddSource,
}

func init() {
	addSourceCmd.Flags().String("type", "auto", "Source type: article, paper, code, auto")
}

func runAddSource(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	relPath := args[0]
	srcType, _ := cmd.Flags().GetString("type")

	// SPEC-08 AC1: the manifest key is a workspace-relative name — reject
	// traversal/malformed inputs, then containment-check the resolved path.
	if err := pathsafe.ValidateRel(relPath); err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("add-source: %w", err))
	}
	absPath, err := pathsafe.SafeJoin(dir, relPath)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("add-source: %w", err))
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("file not found: %s", relPath))
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(data))

	if srcType == "" || srcType == "auto" {
		srcType = "article"
	}

	// Manifest RMW under the exclusive lock (D4): a CLI `add-source` during a
	// running `serve` is the same lost-update class as concurrent MCP writes.
	if err := manifest.Mutate(context.Background(), filepath.Join(dir, ".manifest.json"), func(mf *manifest.Manifest) error {
		mf.AddSource(relPath, hash, srcType, info.Size())
		return nil
	}); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	msg := fmt.Sprintf("Source added: %s (type: %s, %d bytes)", relPath, srcType, info.Size())
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{"path": relPath, "type": srcType}, ""))
	} else {
		fmt.Println(msg)
	}
	return nil
}
