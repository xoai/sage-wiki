package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/manifest"
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

	absPath := filepath.Join(dir, relPath)
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

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	mf.AddSource(relPath, hash, srcType, info.Size())
	if err := mf.Save(filepath.Join(dir, ".manifest.json")); err != nil {
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
