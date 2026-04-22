package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/manifest"
)

var coverageCmd = &cobra.Command{
	Use:   "coverage",
	Short: "Show source compilation coverage",
	RunE:  runCoverage,
}

func init() {
	rootCmd.AddCommand(coverageCmd)
}

type coverageRow struct {
	Path     string `json:"path"`
	Compiled string `json:"compiled"`
}

func runCoverage(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("load manifest: %w", err))
	}

	var rows []coverageRow
	for path, src := range mf.Sources {
		rows = append(rows, coverageRow{
			Path:     path,
			Compiled: src.Status,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, rows, ""))
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("No sources found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tCOMPILED")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\n", r.Path, r.Compiled)
	}
	w.Flush()
	return nil
}
