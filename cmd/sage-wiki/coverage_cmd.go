package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/facts"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/storage"
)

var coverageCmd = &cobra.Command{
	Use:   "coverage",
	Short: "Show three-layer coverage report (extracted/compiled/facts)",
	RunE:  runCoverage,
}

func init() {
	coverageCmd.Flags().String("entity", "", "Filter by entity")

	rootCmd.AddCommand(coverageCmd)
}

type coverageRow struct {
	Path      string `json:"path"`
	Extracted string `json:"extracted"` // yes/no/n-a
	Compiled  string `json:"compiled"`  // compiled/pending/error/missing
	Facts     string `json:"facts"`     // count or n/a
}

func runCoverage(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	// 加载 manifest
	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return fmt.Errorf("load manifest: %w", err)
	}

	// 检查 .pre-extracted/
	preDir := filepath.Join(dir, ".pre-extracted", "files")
	hasPreExtracted := false
	if info, err := os.Stat(preDir); err == nil && info.IsDir() {
		hasPreExtracted = true
	}

	// 打开 facts 表
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	var factsStore *facts.Store
	var db *storage.DB
	if _, err := os.Stat(dbPath); err == nil {
		db, err = storage.Open(dbPath)
		if err == nil {
			factsStore = facts.NewStore(db)
			defer db.Close()
		}
	}

	var rows []coverageRow
	for path, src := range mf.Sources {
		row := coverageRow{
			Path:     path,
			Compiled: src.Status,
		}

		// extracted 层
		if hasPreExtracted {
			relPath := path
			if strings.HasPrefix(relPath, "raw/") {
				relPath = relPath[4:]
			}
			mdPath := filepath.Join(preDir, relPath+".md")
			if _, err := os.Stat(mdPath); err == nil {
				row.Extracted = "yes"
			} else {
				row.Extracted = "no"
			}
		} else {
			row.Extracted = "n/a"
		}

		// facts 层
		if factsStore != nil {
			results, err := factsStore.Query(facts.QueryOpts{Source: path, Limit: 10000})
			if err == nil {
				row.Facts = fmt.Sprintf("%d", len(results))
			} else {
				row.Facts = "error"
			}
		} else {
			row.Facts = "n/a"
		}

		rows = append(rows, row)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, rows, ""))
		return nil
	}

	if len(rows) == 0 {
		fmt.Println("No sources found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tEXTRACTED\tCOMPILED\tFACTS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Path, r.Extracted, r.Compiled, r.Facts)
	}
	w.Flush()
	return nil
}
