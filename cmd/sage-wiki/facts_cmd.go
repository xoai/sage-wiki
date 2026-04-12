package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/facts"
	"github.com/xoai/sage-wiki/internal/storage"
	"gopkg.in/yaml.v3"
)

var factsCmd = &cobra.Command{
	Use:   "facts",
	Short: "Manage structured numeric facts",
}

var factsImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import .numbers.yaml files from .pre-extracted/",
	RunE:  runFactsImport,
}

var factsQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Query facts with filters",
	RunE:  runFactsQuery,
}

var factsStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show facts summary statistics",
	RunE:  runFactsStats,
}

var factsDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete facts by source file",
	RunE:  runFactsDelete,
}

func init() {
	// import flags
	factsImportCmd.Flags().String("aliases", "", "Path to facts-aliases.yaml")

	// query flags
	factsQueryCmd.Flags().String("entity", "", "Filter by entity")
	factsQueryCmd.Flags().String("entity-type", "", "Filter by entity type")
	factsQueryCmd.Flags().String("period", "", "Filter by period")
	factsQueryCmd.Flags().String("label", "", "Filter by semantic label")
	factsQueryCmd.Flags().String("number-type", "", "Filter by number type")
	factsQueryCmd.Flags().String("source", "", "Filter by source file")
	factsQueryCmd.Flags().Int("limit", 100, "Maximum results")
	factsQueryCmd.Flags().Bool("count-only", false, "Only show count")
	factsQueryCmd.Flags().Bool("fuzzy", false, "Fuzzy match entity and label (LIKE %keyword%)")

	// delete flags
	factsDeleteCmd.Flags().String("source", "", "Source file to delete facts for")
	factsDeleteCmd.Flags().Bool("all", false, "Delete all facts")

	factsCmd.AddCommand(factsImportCmd, factsQueryCmd, factsStatsCmd, factsDeleteCmd)
}

func openFactsStore(dir string) (*storage.DB, *facts.Store, error) {
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, nil, err
	}
	return db, facts.NewStore(db), nil
}

func runFactsImport(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	aliasesPath, _ := cmd.Flags().GetString("aliases")

	db, store, err := openFactsStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	var aliases *facts.Aliases
	if aliasesPath == "" {
		// 默认查找 facts-aliases.yaml
		aliasesPath = filepath.Join(dir, "facts-aliases.yaml")
	}
	if data, err := os.ReadFile(aliasesPath); err == nil {
		aliases = &facts.Aliases{}
		if err := yaml.Unmarshal(data, aliases); err != nil {
			return fmt.Errorf("parse aliases %s: %w", aliasesPath, err)
		}
	}

	report, err := facts.ImportDir(store, dir, aliases)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, report, ""))
		return nil
	}

	fmt.Printf("Import: %d added, %d skipped, %d errors (%d files)\n",
		report.Added, report.Skipped, report.Errors, report.Files)
	return nil
}

func runFactsQuery(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, store, err := openFactsStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	entity, _ := cmd.Flags().GetString("entity")
	entityType, _ := cmd.Flags().GetString("entity-type")
	period, _ := cmd.Flags().GetString("period")
	label, _ := cmd.Flags().GetString("label")
	numberType, _ := cmd.Flags().GetString("number-type")
	source, _ := cmd.Flags().GetString("source")
	limit, _ := cmd.Flags().GetInt("limit")
	countOnly, _ := cmd.Flags().GetBool("count-only")
	fuzzy, _ := cmd.Flags().GetBool("fuzzy")

	results, err := store.Query(facts.QueryOpts{
		Entity:     entity,
		EntityType: entityType,
		Period:     period,
		Label:      label,
		NumberType: numberType,
		Source:     source,
		Limit:      limit,
		Fuzzy:      fuzzy,
	})
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if countOnly {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]int{"count": len(results)}, ""))
		} else {
			fmt.Printf("Count: %d\n", len(results))
		}
		return nil
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No facts found.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ENTITY\tPERIOD\tLABEL\tVALUE\tSOURCE")
	for _, f := range results {
		src := f.SourceFile
		if len(src) > 30 {
			src = "..." + src[len(src)-27:]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", f.Entity, f.Period, f.SemanticLabel, f.Value, src)
	}
	w.Flush()
	return nil
}

func runFactsStats(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, store, err := openFactsStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	stats, err := store.Stats()
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, stats, ""))
		return nil
	}

	fmt.Printf("Facts statistics:\n")
	fmt.Printf("  Total facts:     %d\n", stats.TotalFacts)
	fmt.Printf("  Unique entities: %d\n", stats.UniqueEntities)
	fmt.Printf("  Unique periods:  %d\n", stats.UniquePeriods)
	fmt.Printf("  Unique labels:   %d\n", stats.UniqueLabels)
	fmt.Printf("  Unique sources:  %d\n", stats.UniqueSources)
	return nil
}

func runFactsDelete(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	source, _ := cmd.Flags().GetString("source")
	deleteAll, _ := cmd.Flags().GetBool("all")

	if source == "" && !deleteAll {
		return fmt.Errorf("specify --source <file> or --all")
	}

	db, store, err := openFactsStore(dir)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}
	defer db.Close()

	if deleteAll {
		// 删除所有 facts
		var total int64
		allFacts, _ := store.Query(facts.QueryOpts{Limit: 100000})
		sources := make(map[string]bool)
		for _, f := range allFacts {
			sources[f.SourceFile] = true
		}
		for src := range sources {
			n, err := store.DeleteBySource(src)
			if err != nil {
				return err
			}
			total += n
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]int64{"deleted": total}, ""))
		} else {
			fmt.Printf("Deleted %d facts.\n", total)
		}
		return nil
	}

	deleted, err := store.DeleteBySource(source)
	if err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
			return nil
		}
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]int64{"deleted": deleted}, ""))
	} else {
		fmt.Printf("Deleted %d facts from %s.\n", deleted, source)
	}
	return nil
}
