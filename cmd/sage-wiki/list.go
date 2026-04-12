package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List wiki entities, concepts, or sources",
	RunE:  runList,
}

func init() {
	listCmd.Flags().String("type", "", "Filter: concepts, sources, or entity type (concept, technique, etc.)")
}

func runList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	listType, _ := cmd.Flags().GetString("type")

	// Sources: manifest only, no DB
	if listType == "sources" {
		mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		type sourceItem struct {
			Path   string `json:"path"`
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		items := make([]sourceItem, 0, len(mf.Sources))
		for path, s := range mf.Sources {
			items = append(items, sourceItem{Path: path, Type: s.Type, Status: s.Status})
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]any{"items": items, "total": len(items)}, ""))
		} else {
			fmt.Printf("Sources: %d total\n", len(items))
			for _, item := range items {
				fmt.Printf("  [%s] %s (%s)\n", item.Status, item.Path, item.Type)
			}
		}
		return nil
	}

	// Entities: need DB + ontology
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ont := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))

	entityType := listType
	if listType == "concepts" {
		entityType = "concept"
	}

	entities, err := ont.ListEntities(entityType)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	mf, _ := manifest.Load(filepath.Join(dir, ".manifest.json"))

	type listItem struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		ArticlePath string `json:"article_path,omitempty"`
	}
	items := make([]listItem, len(entities))
	for i, e := range entities {
		items[i] = listItem{ID: e.ID, Type: e.Type, Name: e.Name, ArticlePath: e.ArticlePath}
	}

	result := map[string]any{
		"entities":      items,
		"total":         len(items),
		"source_count":  mf.SourceCount(),
		"concept_count": mf.ConceptCount(),
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
	} else {
		fmt.Printf("Entities: %d (sources: %d, concepts: %d)\n", len(items), mf.SourceCount(), mf.ConceptCount())
		for _, item := range items {
			fmt.Printf("  [%s] %s", item.Type, item.Name)
			if item.ArticlePath != "" {
				fmt.Printf(" -> %s", item.ArticlePath)
			}
			fmt.Println()
		}
	}
	return nil
}
