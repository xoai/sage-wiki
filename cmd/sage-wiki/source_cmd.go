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

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Inspect source files",
}

var sourceShowCmd = &cobra.Command{
	Use:   "show <path>",
	Short: "Show source metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runSourceShow,
}

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sources with compilation status",
	RunE:  runSourceList,
}

func init() {
	sourceCmd.AddCommand(sourceShowCmd, sourceListCmd)
}

func runSourceShow(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	relPath := args[0]

	absPath := filepath.Join(dir, relPath)
	info, statErr := os.Stat(absPath)
	if statErr != nil {
		msg := fmt.Sprintf("source not found: %s", relPath)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, msg))
			return nil
		}
		return fmt.Errorf("%s", msg)
	}

	result := map[string]interface{}{
		"path":       relPath,
		"size_bytes": info.Size(),
	}
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
		return nil
	}
	fmt.Printf("Source: %s\n", relPath)
	fmt.Printf("  Size: %d bytes\n", info.Size())
	return nil
}

func runSourceList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return fmt.Errorf("load manifest: %w", err)
	}

	type sourceRow struct {
		Path     string `json:"path"`
		Compiled string `json:"compiled"` // yes/pending/error
		Type     string `json:"type"`
	}

	var rows []sourceRow
	for path, src := range mf.Sources {
		rows = append(rows, sourceRow{
			Path:     path,
			Compiled: src.Status,
			Type:     src.Type,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path }) // SPEC-04 D1

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, rows, ""))
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("No sources found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tCOMPILED\tTYPE")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.Path, r.Compiled, r.Type)
	}
	w.Flush()
	return nil
}
