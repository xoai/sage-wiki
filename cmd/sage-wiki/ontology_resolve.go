package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
)

var ontologyResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Review, apply or reject entity-resolution proposals",
	Long: `Manage entity-resolution links (P3-3).

Linking is non-destructive: the canonical gains copies of the alias's edges and
BOTH entity rows survive. --review lists proposals the compiler queued for a
human; --apply and --reject decide one. --sweep re-applies every approved link,
copying forward any edge that landed on an alias since the last compile — it
makes no LLM calls.`,
	RunE: runOntologyResolve,
}

func init() {
	ontologyResolveCmd.Flags().Bool("review", false, "List pending proposals")
	ontologyResolveCmd.Flags().String("apply", "", "Apply the pending proposal for this alias id")
	ontologyResolveCmd.Flags().String("reject", "", "Reject the pending proposal for this alias id")
	ontologyResolveCmd.Flags().Bool("sweep", false, "Re-apply every approved link (no LLM calls)")
	ontologyCmd.AddCommand(ontologyResolveCmd)
}

// openResolveStore opens the ontology store through the backend-agnostic seam.
//
// NOT storedial.OpenConcrete, which the sibling subcommands use: it unwraps to
// the concrete sqlite *storage.DB and returns "unexpected backend type" under
// backend: postgres. And not storedial.OpenProject either — that hard-codes
// <dir>/config.yaml, so the global --config flag would be ignored here while
// `ontology list` under the same flag reads a different database.
//
// ModeWriter is mandatory: --apply and --sweep write, and a reader handle
// returns a read-only error from every write path.
func openResolveStore(dir string) (store.Backend, store.OntologyStore, error) {
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return nil, nil, err
	}
	lt, err := cfg.Storage.LockTimeoutDuration()
	if err != nil {
		return nil, nil, err
	}
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	b, err := storedial.Open(cfg.Storage, store.OpenOptions{
		Mode:             store.ModeWriter,
		ProjectDir:       dir,
		LockTimeout:      lt,
		Pool:             store.PoolConfig{MaxOpen: cfg.Storage.Pool.MaxOpen, MaxIdle: cfg.Storage.Pool.MaxIdle},
		VectorDimension:  cfg.Storage.VectorDimension,
		ValidRelations:   ontology.ValidRelationNames(mergedRels),
		ValidEntityTypes: ontology.ValidEntityTypeNames(mergedTypes),
	})
	if err != nil {
		return nil, nil, err
	}
	return b, b.Ontology(), nil
}

func runOntologyResolve(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	review, _ := cmd.Flags().GetBool("review")
	sweep, _ := cmd.Flags().GetBool("sweep")
	applyID, _ := cmd.Flags().GetString("apply")
	rejectID, _ := cmd.Flags().GetString("reject")

	// Exactly one action. Two would be ambiguous about ordering; zero is almost
	// always a mistyped flag rather than a request to do nothing.
	n := 0
	for _, set := range []bool{review, sweep, applyID != "", rejectID != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"exactly one of --review, --apply, --reject or --sweep is required (got %d)", n))
	}

	b, ont, err := openResolveStore(dir)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer b.Close()

	switch {
	case review:
		return resolveReview(ont)
	case sweep:
		return resolveSweep(ont)
	case applyID != "":
		return resolveApply(ont, applyID)
	default:
		return resolveReject(ont, rejectID)
	}
}

func resolveReview(ont store.OntologyStore) error {
	pending, err := ont.ListAliases(store.AliasPending)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, pending, ""))
		return nil
	}
	if len(pending) == 0 {
		fmt.Println("No pending entity-resolution proposals.")
		return nil
	}
	fmt.Printf("%d pending proposal(s):\n\n", len(pending))
	for _, p := range pending {
		fmt.Printf("  %s\n    → canonical: %s\n    type: %s  confidence: %.2f\n    reason: %s\n\n",
			p.Alias, p.CanonicalID, p.EntityType, p.Confidence, p.Reason)
	}
	fmt.Println("Apply one:  sage-wiki ontology resolve --apply  <alias>")
	fmt.Println("Reject one: sage-wiki ontology resolve --reject <alias>")
	return nil
}

func resolveApply(ont store.OntologyStore, alias string) error {
	row, err := ont.GetActiveAlias(alias)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if row == nil {
		return cli.CLIError(outputFormat, fmt.Errorf("no active proposal for %q", alias))
	}
	if row.Status == store.AliasApplied {
		fmt.Printf("%s is already linked to %s.\n", row.Alias, row.CanonicalID)
		return nil
	}

	row.Status = store.AliasApplied
	row.DecidedBy = "user"
	// Refreshed: the pending row carries the model's proposal time, and an
	// applied row that reads decided_by=user with a timestamp from before the
	// user saw it is a misleading audit trail. --reject stamps via
	// SetAliasStatus; this is the matching half.
	row.DecidedAt = time.Now().UTC().Format(time.RFC3339)
	res, err := ont.LinkAlias(*row)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	// Reported honestly rather than as success: a zero LinkResult is otherwise
	// indistinguishable from linking an edgeless entity.
	if res.AliasMissing {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"entity %q no longer exists — nothing to link (the proposal is left in place)", alias))
	}
	if res.CanonicalMissing {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"canonical %q no longer exists — nothing to link (the proposal is left in place)",
			row.CanonicalID))
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, res, ""))
		return nil
	}
	fmt.Printf("Linked %s → %s\n  edges copied: %d  already present: %d  self-loops skipped: %d\n",
		row.Alias, row.CanonicalID, res.Copied, res.Skipped, res.SelfLoops)
	fmt.Println("Both entity rows are retained; nothing was deleted.")
	return nil
}

func resolveReject(ont store.OntologyStore, alias string) error {
	row, err := ont.GetActiveAlias(alias)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if row == nil {
		return cli.CLIError(outputFormat, fmt.Errorf("no active proposal for %q", alias))
	}
	if err := ont.SetAliasStatus(row.Alias, row.CanonicalID, store.AliasRejected, "user"); err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]string{
			"alias": row.Alias, "canonical_id": row.CanonicalID, "status": "rejected",
		}, ""))
		return nil
	}
	fmt.Printf("Rejected %s → %s. This pair will not be proposed again, in either direction.\n",
		row.Alias, row.CanonicalID)
	return nil
}

// resolveSweep re-applies every approved link. Zero LLM calls.
//
// This is the remedy for the coverage gap in the compile path: the resolution
// pass only runs when there is something to compile, so on an up-to-date vault
// edges added by reconcile, MCP, trust promotion or scribe do not reach the
// canonical until the next compile — or until this runs.
func resolveSweep(ont store.OntologyStore) error {
	rows, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	jsonOut := outputFormat == "json"
	copied, skipped, missing, failed := 0, 0, 0, 0
	for _, a := range rows {
		res, err := ont.LinkAlias(a)
		if err != nil {
			failed++
			if !jsonOut {
				fmt.Printf("  ! %s → %s: %v\n", a.Alias, a.CanonicalID, err)
			}
			continue
		}
		if res.AliasMissing || res.CanonicalMissing {
			missing++
			if !jsonOut {
				fmt.Printf("  · %s → %s: endpoint missing (link retained)\n", a.Alias, a.CanonicalID)
			}
			continue
		}
		copied += res.Copied
		skipped += res.Skipped
	}
	if jsonOut {
		fmt.Println(cli.FormatJSON(true, map[string]int{
			"links": len(rows), "copied": copied, "already_present": skipped,
			"endpoint_missing": missing, "failed": failed,
		}, ""))
		return nil
	}
	fmt.Printf("Swept %d link(s): %d edge(s) copied, %d already present, %d with a missing endpoint, %d failed.\n",
		len(rows), copied, skipped, missing, failed)
	return nil
}
