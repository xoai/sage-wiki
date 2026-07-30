package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
)

var ontologyResolveCmd = &cobra.Command{
	Use:   "resolve",
	Short: "Review, apply or reject entity-resolution proposals",
	Long: `Manage entity-resolution links (P3-3).

Linking is non-destructive AND reversible: the canonical gains the alias's edges,
BOTH entity rows survive, and --unlink removes exactly the edges a link caused. --review lists proposals the compiler queued for a
human; --apply and --reject decide one; --unlink undoes an applied one. --sweep
re-applies every approved link,
copying forward any edge that landed on an alias since the last compile — it
makes no LLM calls.`,
	RunE: runOntologyResolve,
}

func init() {
	ontologyResolveCmd.Flags().Bool("review", false, "List pending proposals")
	ontologyResolveCmd.Flags().String("apply", "", "Apply the pending proposal for this alias id")
	ontologyResolveCmd.Flags().String("reject", "", "Reject the pending proposal for this alias id")
	ontologyResolveCmd.Flags().Bool("sweep", false, "Re-apply every approved link (no LLM calls)")
	ontologyResolveCmd.Flags().String("unlink", "", "Undo an applied link for this alias id and reject the pair")
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
	// One shared option literal (P3-7): the same helper OpenProject and the
	// startup reconcile use, so a drift breaks all consumers at once.
	b, err := storedial.OpenWithConfig(cfg, dir, store.ModeWriter)
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
	unlinkID, _ := cmd.Flags().GetString("unlink")

	// Exactly one action. Two would be ambiguous about ordering; zero is almost
	// always a mistyped flag rather than a request to do nothing.
	n := 0
	for _, set := range []bool{review, sweep, applyID != "", rejectID != "", unlinkID != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"exactly one of --review, --apply, --reject, --unlink or --sweep is required (got %d)", n))
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
	case unlinkID != "":
		return resolveUnlink(ont, unlinkID)
	default:
		return resolveReject(ont, rejectID)
	}
}

// resolveUnlink undoes an applied link: it removes exactly the edges that link
// caused, records the pair as rejected so the next compile cannot re-apply it,
// and then re-sweeps.
//
// The sweep runs OUTSIDE UnlinkAlias's transaction, deliberately: it loops
// LinkAlias, each of which takes its own WriteTx, and that mutex is not
// reentrant — calling it from inside would deadlock, not error. A failure
// between the two leaves derived rows that the next sweep corrects.
func resolveUnlink(ont store.OntologyStore, alias string) error {
	row, err := ont.GetActiveAlias(alias)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if row == nil {
		return cli.CLIError(outputFormat, fmt.Errorf("no active link for alias %q", alias))
	}
	if row.Status != store.AliasApplied {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"alias %q is %s, not applied — use --reject to decide a pending proposal", alias, row.Status))
	}

	if err := ont.UnlinkAlias(row.Alias, row.CanonicalID); err != nil {
		return cli.CLIError(outputFormat, err)
	}
	// Rebuild: rows derived transitively under this link are stamped with the
	// INTERMEDIATE alias, so deleting by cause alone cannot reach them.
	sweep := compiler.SweepAliases(context.Background(), ont)

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]any{
			"alias": row.Alias, "canonical": row.CanonicalID,
			"status": "rejected", "resweep_rows": sweep.Rows, "resweep_copied": sweep.Copied,
		}, ""))
		return nil
	}
	fmt.Printf("✓ unlinked %s → %s (pair rejected; %d surviving link(s) swept)\n",
		row.Alias, row.CanonicalID, sweep.Rows)
	return nil
}

func resolveReview(ont store.OntologyStore) error {
	pending, err := ont.ListAliases(store.AliasPending)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if outputFormat == "json" {
		if pending == nil {
			pending = []store.EntityAlias{} // emit [], not null, for `jq '.data[]'`
		}
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
	// The partial unique index is keyed on `alias` alone, so an active A->B row
	// can coexist with a rejected B->A row. Without this check --apply would
	// link a pair the user rejected in the other direction, which the guide
	// promises cannot happen.
	rejected, err := ont.IsRejected(row.Alias, row.CanonicalID)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if rejected {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"%q and %q were rejected as different entities — reject/apply is symmetric, "+
				"so this pair cannot be linked in either direction", row.Alias, row.CanonicalID))
	}
	// The pass refuses a link whose chain-resolved target IS the alias; without
	// the same check here, applying a stale pending row after the reverse link
	// was auto-applied creates a 2-cycle in which neither row is canonical,
	// CanonicalID logs "alias cycle detected" forever, and the sweep copies
	// edges both ways on every compile.
	terminal, err := compiler.TerminalCanonical(ont, row.CanonicalID)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if terminal == row.Alias {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"cannot link %q into %q: %q already resolves back to %q, so this would "+
				"create a cycle in which neither entity is canonical",
			row.Alias, row.CanonicalID, row.CanonicalID, row.Alias))
	}
	// The same co-absorption rule the compile pass applies. Without it, a
	// proposal the pass queued (rather than applied) could be completed here,
	// folding both halves of a rejected pair under one canonical — the exact
	// merge the pass refused, finished by the command that reports rejections
	// are honoured.
	conflict, err := compiler.CoAbsorptionConflict(ont, row.Alias, row.CanonicalID)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if conflict != "" {
		return cli.CLIError(outputFormat, fmt.Errorf(
			"cannot link %q into %q — %s. Linking would settle a pair you own",
			row.Alias, row.CanonicalID, conflict))
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
	wasApplied := row.Status == store.AliasApplied
	if err := ont.SetAliasStatus(row.Alias, row.CanonicalID, store.AliasRejected, "user"); err != nil {
		return cli.CLIError(outputFormat, err)
	}
	// "in either direction" has to be true of the CURRENT state, not only of
	// future proposals. A pending row can coexist with an applied row for the
	// same pair pointing the other way — the election flips as soon as one side
	// gains an article — and rejecting only the pending half would leave the
	// applied link live, with the sweep copying across it on every compile,
	// under a message promising the opposite.
	reverse, err := ont.GetActiveAlias(row.CanonicalID)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	alsoRejected := false
	// Where the derived edges actually landed. When the applied half is the
	// REVERSE row, they sit on that row's canonical — naming row.CanonicalID
	// would point the user at the wrong entity, and --unlink takes an alias id,
	// so following the wrong name would undo the wrong link.
	residueOn := row.CanonicalID
	// Applied OR pending. Clearing only the applied case leaves an unapplicable
	// pending row behind: --review lists it forever, --apply always errors
	// (the pair is now rejected), and its active status freezes the alias out of
	// resolution — leaving --reject on the other half, recording a judgement the
	// user never made, as the only escape.
	if reverse != nil && reverse.CanonicalID == row.Alias {
		if err := ont.SetAliasStatus(reverse.Alias, reverse.CanonicalID, store.AliasRejected, "user"); err != nil {
			return cli.CLIError(outputFormat, err)
		}
		alsoRejected = true
		if reverse.Status == store.AliasApplied {
			wasApplied = true
			residueOn = reverse.CanonicalID
		}
	}
	if outputFormat == "json" {
		payload := map[string]any{
			"alias": row.Alias, "canonical_id": row.CanonicalID, "status": "rejected",
		}
		if alsoRejected {
			payload["reverse_link_rejected"] = true
		}
		// The residue fact belongs on the machine-readable path too: a scripted
		// consumer told only "rejected" has no way to learn that the canonical
		// still holds derived edges, or that --unlink is what removes them.
		if wasApplied {
			payload["edges_remain_on"] = residueOn
		}
		fmt.Println(cli.FormatJSON(true, payload, ""))
		return nil
	}
	fmt.Printf("Rejected %s → %s. This pair will not be proposed again, in either direction.\n",
		row.Alias, row.CanonicalID)
	if alsoRejected {
		fmt.Printf("Also rejected %s → %s, the row pointing the other way.\n",
			row.CanonicalID, row.Alias)
	}
	if wasApplied {
		// Rejecting is not an undo: linking copied edges onto the canonical and
		// those rows are still there. Saying "rejected" without this reads as a
		// rollback that did not happen.
		fmt.Printf("Note: this link had already been APPLIED. Edges copied onto %s remain "+
			"(they carry an \"alias:\" id prefix); rejecting only stops it being re-applied.\n",
			residueOn)
	}
	return nil
}

// resolveSweep re-applies every approved link. Zero LLM calls.
//
// This is the remedy for the coverage gap in the compile path: the resolution
// pass only runs when there is something to compile, so on an up-to-date vault
// edges added by reconcile, MCP, trust promotion or scribe do not reach the
// canonical until the next compile — or until this runs.
func resolveSweep(ont store.OntologyStore) error {
	// Delegates to the compile pass's implementation rather than repeating it.
	// A second copy here is exactly how the rejection filter came to exist on
	// the compile path and not on the command the guide recommends as the
	// remedy — while --reject printed a promise that copy broke.
	res := compiler.SweepAliases(context.Background(), ont)

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, map[string]int{
			"links": res.Rows, "copied": res.Copied,
			"already_present":  res.AlreadyPresent,
			"endpoint_missing": res.EndpointMissing,
			"failed":           res.Failed, "rejected_skipped": res.RejectedSkipped,
		}, ""))
	} else {
		fmt.Printf("Swept %d link(s): %d edge(s) copied, %d already present, "+
			"%d with a missing endpoint, %d skipped as rejected, %d failed.\n",
			res.Rows, res.Copied, res.AlreadyPresent, res.EndpointMissing,
			res.RejectedSkipped, res.Failed)
	}
	if res.Failed > 0 {
		// A scripted sweep must be able to detect breakage from the exit code.
		return cli.CLIError(outputFormat, fmt.Errorf("%d link(s) failed to sweep", res.Failed))
	}
	return nil
}
