package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
)

var ontologyCmd = &cobra.Command{
	Use:   "ontology",
	Short: "Query and manage the ontology graph",
}

var ontologyQueryCmd = &cobra.Command{
	Use:   "query",
	Short: "Traverse the ontology from an entity",
	RunE:  runOntologyQuery,
}

var ontologyAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an entity or relation",
	RunE:  runOntologyAdd,
}

var ontologyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List entities or relations",
	RunE:  runOntologyList,
}

func init() {
	ontologyQueryCmd.Flags().String("entity", "", "Entity ID to start from")
	ontologyQueryCmd.Flags().String("relation", "", "Filter by relation type")
	ontologyQueryCmd.Flags().String("direction", "outbound", "outbound, inbound, or both")
	ontologyQueryCmd.Flags().Int("depth", 1, "Traversal depth 1-5")
	ontologyQueryCmd.MarkFlagRequired("entity")

	ontologyAddCmd.Flags().String("from", "", "Source entity ID (for relations)")
	ontologyAddCmd.Flags().String("to", "", "Target entity ID (for relations)")
	ontologyAddCmd.Flags().String("relation", "", "Relation type")
	ontologyAddCmd.Flags().String("entity-id", "", "Entity ID (for creating entities)")
	ontologyAddCmd.Flags().String("entity-type", "concept", "Entity type")
	ontologyAddCmd.Flags().String("entity-name", "", "Human-readable name")

	ontologyListCmd.Flags().String("type", "entities", "What to list: entities or relations")
	ontologyListCmd.Flags().String("entity-type", "", "Filter entities by type (concept, source, etc.)")
	ontologyListCmd.Flags().String("relation-type", "", "Filter relations by type")
	ontologyListCmd.Flags().Int("limit", 100, "Maximum results")

	ontologyCmd.AddCommand(ontologyQueryCmd, ontologyAddCmd, ontologyListCmd)
}

func openOntStore(dir string) (*storage.DB, *ontology.Store, error) {
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		return nil, nil, err
	}
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return nil, nil, err
	}
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	return db, ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes)), nil
}

func runOntologyQuery(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	entityID, _ := cmd.Flags().GetString("entity")
	relType, _ := cmd.Flags().GetString("relation")
	dirStr, _ := cmd.Flags().GetString("direction")
	depth, _ := cmd.Flags().GetInt("depth")

	db, ont, err := openOntStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	traverseDir := ontology.Outbound
	switch dirStr {
	case "inbound":
		traverseDir = ontology.Inbound
	case "both":
		traverseDir = ontology.Both
	}

	entities, err := ont.Traverse(entityID, ontology.TraverseOpts{
		Direction:    traverseDir,
		RelationType: relType,
		MaxDepth:     depth,
	})
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, entities, ""))
		return nil
	}

	if len(entities) == 0 {
		fmt.Printf("No entities found from %q\n", entityID)
		return nil
	}
	for _, e := range entities {
		fmt.Printf("  [%s] %s (%s)\n", e.Type, e.Name, e.ID)
	}
	return nil
}

func runOntologyAdd(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	fromID, _ := cmd.Flags().GetString("from")
	toID, _ := cmd.Flags().GetString("to")
	relType, _ := cmd.Flags().GetString("relation")
	entityID, _ := cmd.Flags().GetString("entity-id")

	db, ont, err := openOntStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	// Add relation
	if fromID != "" && toID != "" && relType != "" {
		relID := fromID + "-" + relType + "-" + toID
		if err := ont.AddRelation(ontology.Relation{
			ID: relID, SourceID: fromID, TargetID: toID, Relation: relType,
		}); err != nil {
			return cli.CLIError(outputFormat, err)
		}
		msg := fmt.Sprintf("Relation: %s -[%s]-> %s", fromID, relType, toID)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
		} else {
			fmt.Println(msg)
		}
		return nil
	}

	// Add entity
	if entityID != "" {
		entityType, _ := cmd.Flags().GetString("entity-type")
		entityName, _ := cmd.Flags().GetString("entity-name")
		if entityName == "" {
			entityName = entityID
		}
		if err := ont.AddEntity(ontology.Entity{
			ID: entityID, Type: entityType, Name: entityName,
		}); err != nil {
			return cli.CLIError(outputFormat, err)
		}
		msg := fmt.Sprintf("Entity created: %s (%s)", entityID, entityType)
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, map[string]string{"message": msg}, ""))
		} else {
			fmt.Println(msg)
		}
		return nil
	}

	return cli.CLIError(outputFormat, fmt.Errorf("provide --from/--to/--relation for relations, or --entity-id for entities"))
}

func runOntologyList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	listType, _ := cmd.Flags().GetString("type")
	limit, _ := cmd.Flags().GetInt("limit")

	db, ont, err := openOntStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	switch listType {
	case "entities":
		entityType, _ := cmd.Flags().GetString("entity-type")
		entities, err := ont.ListEntities(entityType)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		if limit > 0 && len(entities) > limit {
			entities = entities[:limit]
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, entities, ""))
			return nil
		}
		fmt.Printf("Entities: %d\n", len(entities))
		for _, e := range entities {
			fmt.Printf("  [%s] %s (%s)\n", e.Type, e.Name, e.ID)
		}

	case "relations":
		relType, _ := cmd.Flags().GetString("relation-type")
		rels, err := ont.ListRelations(relType, limit)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, rels, ""))
			return nil
		}
		fmt.Printf("Relations: %d\n", len(rels))
		for _, r := range rels {
			fmt.Printf("  %s -[%s]-> %s\n", r.SourceID, r.Relation, r.TargetID)
		}

	default:
		return fmt.Errorf("unknown list type %q, use 'entities' or 'relations'", listType)
	}
	return nil
}
