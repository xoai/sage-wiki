package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show pending source changes against manifest",
	RunE:  runDiff,
}

func runDiff(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	diff, err := compiler.Diff(dir, cfg, mf)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	type diffData struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
		Pending  int      `json:"pending"`
		Total    int      `json:"total"`
	}

	added := make([]string, len(diff.Added))
	for i, s := range diff.Added {
		added[i] = s.Path
	}
	modified := make([]string, len(diff.Modified))
	for i, s := range diff.Modified {
		modified[i] = s.Path
	}

	data := diffData{
		Added:    added,
		Modified: modified,
		Removed:  diff.Removed,
		Pending:  len(diff.Added) + len(diff.Modified),
		Total:    mf.SourceCount(),
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, data, ""))
		return nil
	}

	if data.Pending == 0 && len(diff.Removed) == 0 {
		fmt.Println("Nothing to compile — wiki is up to date.")
		return nil
	}
	fmt.Printf("Sources: %d total, %d pending\n", data.Total, data.Pending)
	for _, p := range added {
		fmt.Printf("  + %s (new)\n", p)
	}
	for _, p := range modified {
		fmt.Printf("  ~ %s (modified)\n", p)
	}
	for _, p := range diff.Removed {
		fmt.Printf("  - %s (removed)\n", p)
	}
	return nil
}
