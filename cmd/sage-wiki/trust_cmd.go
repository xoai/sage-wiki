package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run grounding checks on pending outputs",
	RunE:  runVerify,
}

var outputsCmd = &cobra.Command{
	Use:   "outputs",
	Short: "Manage output trust state",
}

var outputsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List outputs by trust state",
	RunE:  runOutputsList,
}

var outputsPromoteCmd = &cobra.Command{
	Use:   "promote <id>",
	Short: "Manually promote an output to confirmed",
	Args:  cobra.ExactArgs(1),
	RunE:  runOutputsPromote,
}

var outputsRejectCmd = &cobra.Command{
	Use:   "reject <id>",
	Short: "Reject and delete a pending output",
	Args:  cobra.ExactArgs(1),
	RunE:  runOutputsReject,
}

var outputsMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate existing outputs to the trust system",
	RunE:  runOutputsMigrate,
}

var outputsResolveCmd = &cobra.Command{
	Use:   "resolve <id>",
	Short: "Resolve a conflict by promoting this answer and rejecting others for the same question",
	Args:  cobra.ExactArgs(1),
	RunE:  runOutputsResolve,
}

var outputsCleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Remove stale or old pending outputs",
	RunE:  runOutputsClean,
}

func init() {
	verifyCmd.Flags().Bool("all", false, "Verify all pending outputs")
	verifyCmd.Flags().String("since", "", "Only verify outputs newer than duration (e.g. 7d, 24h)")
	verifyCmd.Flags().String("question", "", "Verify a specific question")
	verifyCmd.Flags().Int("limit", 20, "Maximum outputs to verify")

	outputsListCmd.Flags().String("state", "", "Filter by state: pending, confirmed, conflict, stale")

	outputsCleanCmd.Flags().String("older-than", "90d", "Remove outputs older than this duration")

	outputsCmd.AddCommand(outputsListCmd, outputsPromoteCmd, outputsRejectCmd, outputsMigrateCmd, outputsResolveCmd, outputsCleanCmd)
	rootCmd.AddCommand(verifyCmd, outputsCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	cfgPath := resolveConfigPath(dir)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("LLM client: %w", err))
	}

	store := trust.NewStore(db)

	all, _ := cmd.Flags().GetBool("all")
	sinceStr, _ := cmd.Flags().GetString("since")
	question, _ := cmd.Flags().GetString("question")
	limit, _ := cmd.Flags().GetInt("limit")

	var since time.Duration
	if sinceStr != "" {
		var parseErr error
		since, parseErr = parseDuration(sinceStr)
		if parseErr != nil {
			return cli.CLIError(outputFormat, fmt.Errorf("invalid --since: %w", parseErr))
		}
	}

	stores := buildIndexStores(db)
	opts := trust.VerifyOpts{
		All:                all,
		Since:              since,
		Question:           question,
		Limit:              limit,
		AutoPromote:        cfg.Trust.AutoPromoteEnabled(),
		Threshold:          cfg.Trust.GroundingThresholdOrDefault(),
		ConsensusThreshold: cfg.Trust.ConsensusThresholdOrDefault(),
		Stores:             &stores,
	}

	fmt.Fprintf(os.Stderr, "Verifying pending outputs (limit: %d)...\n", limit)

	model := cfg.Models.Query
	if model == "" {
		model = "gpt-4o-mini"
	}
	results, err := trust.Verify(store, client, model, dir, opts)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No pending outputs to verify.")
		return nil
	}

	promoted := 0
	for _, r := range results {
		status := "pending"
		if r.Promoted {
			status = "promoted"
			promoted++
		}
		if r.Error != nil {
			status = "error"
		}
		fmt.Printf("  %-10s  %.2f  %s  %s\n", status, r.GroundingScore, r.Output.ID, truncate(r.Output.Question, 50))
		if r.Error != nil {
			fmt.Fprintf(os.Stderr, "    error: %v\n", r.Error)
		}
	}
	fmt.Printf("\nVerified: %d, Promoted: %d\n", len(results), promoted)
	return nil
}

func runOutputsList(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	stateFilter, _ := cmd.Flags().GetString("state")

	var outputs []*trust.PendingOutput
	if stateFilter != "" {
		outputs, err = store.ListByState(trust.OutputState(stateFilter))
	} else {
		pending, _ := store.ListByState(trust.StatePending)
		confirmed, _ := store.ListByState(trust.StateConfirmed)
		conflict, _ := store.ListByState(trust.StateConflict)
		stale, _ := store.ListByState(trust.StateStale)
		outputs = append(outputs, pending...)
		outputs = append(outputs, confirmed...)
		outputs = append(outputs, conflict...)
		outputs = append(outputs, stale...)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, outputs, ""))
		return nil
	}

	if len(outputs) == 0 {
		fmt.Println("No outputs found.")
		return nil
	}

	for _, o := range outputs {
		score := "-"
		if o.GroundingScore != nil {
			score = fmt.Sprintf("%.2f", *o.GroundingScore)
		}
		fmt.Printf("  %-10s  score:%s  confirms:%d  %s  %s\n",
			o.State, score, o.Confirmations, o.ID, truncate(o.Question, 40))
	}
	fmt.Printf("\nTotal: %d\n", len(outputs))
	return nil
}

func runOutputsPromote(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	id := args[0]

	o, err := store.Get(id)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("output %q not found", id))
	}

	stores := buildIndexStores(db)
	if err := trust.PromoteOutput(store, id, dir, stores); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	fmt.Printf("Promoted: %s (%s)\n", id, truncate(o.Question, 50))
	return nil
}

func runOutputsReject(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	id := args[0]

	if _, err := store.Get(id); err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("output %q not found", id))
	}

	stores := buildIndexStores(db)
	if err := trust.RejectOutput(store, id, dir, stores); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	fmt.Printf("Rejected: %s\n", id)
	return nil
}

func buildIndexStores(db *storage.DB) trust.IndexStores {
	return trust.IndexStores{
		MemStore:   memory.NewStore(db),
		VecStore:   vectors.NewStore(db),
		OntStore:   ontology.NewStore(db, nil, nil),
		ChunkStore: memory.NewChunkStore(db),
		DB:         db,
	}
}

func runOutputsMigrate(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	cfgPath := resolveConfigPath(dir)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	ontStore := ontology.NewStore(db, nil, nil)
	chunkStore := memory.NewChunkStore(db)

	stores := trust.IndexStores{
		MemStore:   memStore,
		VecStore:   vecStore,
		OntStore:   ontStore,
		ChunkStore: chunkStore,
		DB:         db,
	}

	result, err := trust.MigrateExistingOutputs(store, dir, cfg.Output, func(id string) {
		trust.DeindexOutput(id, stores)
	})
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
		return nil
	}

	fmt.Printf("Migration complete: %d migrated, %d skipped (already in trust system)\n", result.Migrated, result.Skipped)
	return nil
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("too short: %q", s)
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid number: %q", numStr)
	}

	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return time.ParseDuration(s)
	}
}

func runOutputsResolve(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	id := args[0]

	winner, err := store.Get(id)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("output %q not found", id))
	}

	stores := buildIndexStores(db)
	if err := trust.PromoteOutput(store, id, dir, stores); err != nil {
		return cli.CLIError(outputFormat, err)
	}

	siblings, err := store.ListByQuestionHash(winner.QuestionHash)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	rejected := 0
	for _, s := range siblings {
		if s.ID == id {
			continue
		}
		if err := trust.RejectOutput(store, s.ID, dir, stores); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: failed to reject %s: %v\n", s.ID, err)
			continue
		}
		rejected++
	}

	fmt.Printf("Resolved: promoted %s, rejected %d competing answers\n", id, rejected)
	return nil
}

func runOutputsClean(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer db.Close()

	store := trust.NewStore(db)
	olderThanStr, _ := cmd.Flags().GetString("older-than")

	duration, err := parseDuration(olderThanStr)
	if err != nil {
		return cli.CLIError(outputFormat, fmt.Errorf("invalid --older-than: %w", err))
	}

	cutoff := time.Now().Add(-duration)
	old, err := store.ListOlderThan(cutoff)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}

	stores := buildIndexStores(db)
	cleaned := 0
	for _, o := range old {
		trust.RejectOutput(store, o.ID, dir, stores)
		cleaned++
	}

	fmt.Printf("Cleaned: %d stale/pending outputs older than %s\n", cleaned, olderThanStr)
	return nil
}
