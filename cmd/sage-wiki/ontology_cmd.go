package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/trust"
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
	ontologyQueryCmd.Flags().String("as-of", "", "RFC3339 timestamp: traverse only edges valid at that time (P3-6)")
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

func openOntStore(dir string) (*storage.DB, *ontology.Store, *config.Config, error) {
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := storedial.OpenConcrete(dir, cfg.Storage)
	if err != nil {
		return nil, nil, nil, err
	}
	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	return db, ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
		ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault())), cfg, nil
}

func runOntologyQuery(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	entityID, _ := cmd.Flags().GetString("entity")
	relType, _ := cmd.Flags().GetString("relation")
	dirStr, _ := cmd.Flags().GetString("direction")
	depth, _ := cmd.Flags().GetInt("depth")

	db, ont, cfg, err := openOntStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	var asOf time.Time
	if raw, _ := cmd.Flags().GetString("as-of"); raw != "" {
		if !cfg.Ontology.Temporal.EnabledOrDefault() {
			return cli.CLIError(outputFormat, fmt.Errorf("--as-of requires ontology.temporal.enabled (currently false)"))
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return cli.CLIError(outputFormat, fmt.Errorf("invalid --as-of %q: expected RFC3339 (e.g. 2026-01-15T00:00:00Z)", raw))
		}
		asOf = t
	}

	traverseDir := ontology.Outbound
	switch dirStr {
	case "inbound":
		traverseDir = ontology.Inbound
	case "both":
		traverseDir = ontology.Both
	}

	// --entity may name an alias; traverse from its canonical. The notice goes
	// to stderr so stdout stays one valid JSON document under --format json.
	if resolved := store.CanonicalOrSelf(ont, entityID); resolved != entityID {
		fmt.Fprintf(os.Stderr, "note: %q is an alias of %q; traversing from the canonical entity\n", entityID, resolved)
		entityID = resolved
	}

	entities, err := ont.Traverse(entityID, ontology.TraverseOpts{
		Direction:    traverseDir,
		RelationType: relType,
		MaxDepth:     depth,
		AsOf:         asOf,
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

	db, ont, cfg, err := openOntStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	// Add relation
	if fromID != "" && toID != "" && relType != "" {
		relID := fromID + "-" + relType + "-" + toID
		if err := ont.AddRelation(ontology.Relation{
			ID: relID, SourceID: fromID, TargetID: toID, Relation: relType,
			ValidFrom: time.Now().UTC().Format(time.RFC3339), // manual add = asserted now (P3-6)
		}); err != nil {
			return cli.CLIError(outputFormat, err)
		}
		msg := fmt.Sprintf("Relation: %s -[%s]-> %s", fromID, relType, toID)
		// P3-6: same rule as wiki_ontology_add — functional predicates
		// auto-apply supersession (manual add = explicit intent); bare
		// contradicts edges surface a dedup'd trust conflict for review.
		if cfg.Ontology.Temporal.EnabledOrDefault() {
			if relType == ontology.RelContradicts {
				cliEmitEdgeConflict(db,
					fmt.Sprintf("Edge conflict: %s contradicts %s (source: manual add)", fromID, toID),
					"Deferred: entity-level contradicts edge recorded for review; no auto-invalidation.")
				msg += " (conflict recorded for review)"
			} else if cliFunctionalPredicate(cfg, relType) {
				invalidated, err := ont.InvalidateFunctional(fromID, relType, toID,
					time.Now().UTC().Format(time.RFC3339), relID)
				if err != nil {
					return cli.CLIError(outputFormat, err)
				}
				if len(invalidated) > 0 {
					msg += fmt.Sprintf(" (superseded %d prior edge(s))", len(invalidated))
				}
			}
		}
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

	db, ont, _, err := openOntStore(dir)
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

// cliFunctionalPredicate reports whether relType is configured functional
// (outbound uniqueness, P3-6) in either relation config key.
func cliFunctionalPredicate(cfg *config.Config, relType string) bool {
	// config.Load normalizes relation_types into Relations — one loop only.
	for _, rc := range cfg.Ontology.Relations {
		if rc.Name == relType && rc.Functional {
			return true
		}
	}
	return false
}

// cliEmitEdgeConflict records a trust conflict for a manually added
// contradicts edge (P3-6), mirroring the MCP emitter: deterministic ID
// dedups repeats; insert races lose to the PK and are swallowed.
func cliEmitEdgeConflict(db *storage.DB, question, answer string) {
	ts := trust.NewStore(db)
	sum := sha256.Sum256([]byte(question))
	id := "edgeconflict-" + hex.EncodeToString(sum[:])[:16]
	if existing, err := ts.Get(id); err == nil && existing != nil {
		return
	}
	if err := ts.InsertPending(&store.PendingOutput{
		ID:           id,
		Question:     question,
		QuestionHash: trust.HashQuestion(question),
		Answer:       answer,
		AnswerHash:   trust.HashAnswer(answer),
		State:        store.StateConflict,
		SourcesUsed:  "[]",
		SourcesHash:  trust.ComputeSourcesHash("", "[]"),
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "note: conflict record skipped: %v\n", err)
	}
}
