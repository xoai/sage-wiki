package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/log"
	"strings"

	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/pack"
	"github.com/xoai/sage-wiki/internal/prompts"
	tuidashboard "github.com/xoai/sage-wiki/internal/tui/dashboard"
	"github.com/xoai/sage-wiki/internal/web"
	"github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/scribe"
	"github.com/xoai/sage-wiki/internal/skill"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
)

var (
	projectDir   string
	configPath   string
	verbosity    int
	outputFormat string
)

// Build metadata, injected at release time via
// -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// Defaults are non-blank so `sage-wiki version` from a plain `go build` is
// still readable.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true

	if err := rootCmd.Execute(); err != nil {
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(false, nil, err.Error()))
		} else {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "sage-wiki",
	Short: "LLM-compiled personal knowledge base",
	Long:  "sage-wiki compiles raw documents into a structured, interlinked markdown wiki using LLM agents.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		log.SetVerbosity(verbosity)
	},
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new sage-wiki project",
	RunE:  runInit,
}

var compileCmd = &cobra.Command{
	Use:   "compile",
	Short: "Compile sources into wiki articles",
	RunE:  runCompile,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP server",
	RunE:  runServe,
}

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Run linting passes on the wiki",
	RunE:  runLint,
}

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search the wiki",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

var queryCmd = &cobra.Command{
	Use:   "query [question]",
	Short: "Ask a question against the wiki",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runQuery,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show wiki stats and health",
	RunE:  runStatus,
}

var ingestCmd = &cobra.Command{
	Use:   "ingest [url-or-path]",
	Short: "Add a source to the wiki",
	Args:  cobra.ExactArgs(1),
	RunE:  runIngest,
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Validate configuration and connectivity",
	RunE:  runDoctor,
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch interactive terminal dashboard",
	RunE:  runTUI,
}

var provenanceCmd = &cobra.Command{
	Use:   "provenance [source-or-concept]",
	Short: "Show source-article provenance mappings",
	Long:  "Given a source path, shows generated articles. Given a concept name, shows contributing sources.",
	Args:  cobra.ExactArgs(1),
	RunE:  runProvenance,
}

var scribeCmd = &cobra.Command{
	Use:   "scribe <session-file>",
	Short: "Extract knowledge entities from a session transcript",
	Long:  "Process a Claude Code session JSONL file, extract entities, and add them to the wiki ontology.\n\nUsage: sage-wiki scribe path/to/session.jsonl",
	Args:  cobra.ExactArgs(1),
	RunE:  runScribe,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	RunE:  runVersion,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&projectDir, "project", ".", "Project directory")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Config file path (default: <project>/config.yaml)")
	rootCmd.PersistentFlags().CountVarP(&verbosity, "verbose", "v", "Increase log verbosity (-v for info, -vv for debug)")
	rootCmd.PersistentFlags().StringVar(&outputFormat, "format", "text", "Output format: text or json")

	// Init flags
	initCmd.Flags().Bool("vault", false, "Initialize as vault overlay on existing Obsidian vault")
	initCmd.Flags().Bool("prompts", false, "Scaffold prompt templates for customization")
	initCmd.Flags().String("model", "gemini-2.5-flash", "Default LLM model for all tasks (e.g. gemini-2.5-flash, gemini-3.1-flash-lite)")
	initCmd.Flags().String("skill", "", "Generate agent skill file (claude-code, cursor, windsurf, agents-md, codex, gemini, generic)")
	initCmd.Flags().String("pack", "", "Install a contribution pack during init")

	// Compile flags
	compileCmd.Flags().Bool("watch", false, "Watch for changes and recompile")
	compileCmd.Flags().Bool("dry-run", false, "Show what would change without writing")
	compileCmd.Flags().Bool("fresh", false, "Clear checkpoint state (batch + legacy) and recompile from scratch")
	compileCmd.Flags().Bool("re-embed", false, "Re-generate embeddings for all entries without recompiling")
	compileCmd.Flags().Bool("re-extract", false, "Re-run concept extraction and article writing from existing summaries")
	compileCmd.Flags().Bool("estimate", false, "Show cost estimate without compiling")
	compileCmd.Flags().Bool("batch", false, "Use batch API for 50% cost reduction (async)")
	compileCmd.Flags().Bool("no-cache", false, "Disable prompt caching for this run")
	compileCmd.Flags().Bool("prune", false, "Delete orphaned articles when their sole source is removed")

	// Serve flags
	serveCmd.Flags().String("transport", "stdio", "Transport: stdio or sse")
	serveCmd.Flags().Int("port", 3333, "SSE/UI port")
	serveCmd.Flags().Bool("ui", false, "Start web UI viewer")
	serveCmd.Flags().String("bind", "127.0.0.1", "Bind address (default localhost only)")
	serveCmd.Flags().String("token", "", "Bearer token gating /api/* and /ws (or SAGE_WIKI_TOKEN env); mandatory for non-loopback binds")
	serveCmd.Flags().String("allowed-host", "", "Comma-separated extra Host values accepted beyond loopback (or SAGE_WIKI_ALLOWED_HOST env; anti DNS-rebind)")

	// Lint flags
	lintCmd.Flags().Bool("fix", false, "Auto-fix issues")
	lintCmd.Flags().String("pass", "", "Run specific lint pass")
	lintCmd.Flags().Bool("dry-run", false, "Show findings without fixing")

	// Search flags
	searchCmd.Flags().StringSlice("tags", nil, "Filter by tags")
	searchCmd.Flags().Int("limit", 10, "Maximum results")
	searchCmd.Flags().String("scope", "local", "Search scope: local, global, or all")

	// Query flags
	queryCmd.Flags().String("scope", "local", "Query scope: local, global, or all")

	rootCmd.AddCommand(initCmd, compileCmd, serveCmd, lintCmd, searchCmd, queryCmd, statusCmd, ingestCmd, doctorCmd, tuiCmd, provenanceCmd, scribeCmd, diffCmd, listCmd, ontologyCmd, writeCmd, learnCmd, captureCmd, addSourceCmd, sourceCmd, hubCmd, skillCmd, packCmd, versionCmd)

	// Enables `sage-wiki --version` in addition to the `version` subcommand.
	rootCmd.Version = version
}

func runVersion(cmd *cobra.Command, args []string) error {
	if outputFormat == "json" {
		fmt.Fprintln(cmd.OutOrStdout(), cli.FormatJSON(true, map[string]string{
			"version": version,
			"commit":  commit,
			"date":    date,
		}, ""))
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "sage-wiki %s (commit %s, built %s)\n", version, commit, date)
	return nil
}

func resolveConfigPath(dir string) string {
	if configPath != "" {
		if filepath.IsAbs(configPath) {
			return configPath
		}
		return filepath.Join(dir, configPath)
	}
	return filepath.Join(dir, "config.yaml")
}

func runInit(cmd *cobra.Command, args []string) error {
	vaultMode, _ := cmd.Flags().GetBool("vault")
	model, _ := cmd.Flags().GetString("model")
	dir, _ := filepath.Abs(projectDir)

	// Derive project name from directory
	project := filepath.Base(dir)

	// If --skill is set on an already-initialized project, skip project creation
	skillTarget, _ := cmd.Flags().GetString("skill")
	cfgPath := resolveConfigPath(dir)
	_, cfgExists := os.Stat(cfgPath)
	skipInit := skillTarget != "" && cfgExists == nil

	if skipInit {
		fmt.Printf("Project already initialized. Generating skill file only.\n")
	} else if vaultMode {
		// Scan folders for interactive selection
		folders, err := wiki.ScanFolders(dir)
		if err != nil {
			return fmt.Errorf("failed to scan vault: %w", err)
		}

		if len(folders) == 0 {
			return fmt.Errorf("no folders found in %s", dir)
		}

		fmt.Printf("Detected vault: %s\n", project)
		fmt.Printf("Found %d folders:\n\n", len(folders))

		var sourceFolders, ignoreFolders []string
		for _, f := range folders {
			desc := fmt.Sprintf("  %s/ (%d files", f.Name, f.FileCount)
			if f.HasPDF {
				desc += ", has PDFs"
			}
			desc += ")"
			fmt.Println(desc)

			// Default: folders with content are sources, others ignored
			if f.FileCount > 0 {
				sourceFolders = append(sourceFolders, f.Name)
			} else {
				ignoreFolders = append(ignoreFolders, f.Name)
			}
		}

		fmt.Printf("\nSource folders: %v\n", sourceFolders)
		fmt.Printf("Ignored folders: %v\n", ignoreFolders)
		fmt.Println("\nEdit config.yaml to adjust source/ignore folders.")

		if err := wiki.InitVaultOverlay(dir, project, sourceFolders, ignoreFolders, "_wiki", model); err != nil {
			return err
		}
	} else {
		if err := wiki.InitGreenfield(dir, project, model); err != nil {
			return err
		}
	}

	// Scaffold prompt templates if requested
	scaffoldPrompts, _ := cmd.Flags().GetBool("prompts")
	if scaffoldPrompts {
		promptsDir := filepath.Join(dir, "prompts")
		if err := prompts.ScaffoldDefaults(promptsDir); err != nil {
			return fmt.Errorf("failed to scaffold prompts: %w", err)
		}
		fmt.Printf("Prompt templates scaffolded in prompts/\n")
		fmt.Printf("Edit these files to customize how sage-wiki summarizes and writes articles.\n")
	}

	// Install and apply contribution pack if requested
	packFlag, _ := cmd.Flags().GetString("pack")
	if packFlag != "" {
		fmt.Printf("Installing pack %q...\n", packFlag)
		installSource := "local"
		manifest, packDir, err := pack.Install(packFlag, "")
		if err != nil {
			if pack.ValidateName(packFlag) == nil && !isPathOrURL(packFlag) {
				reg := pack.NewRegistry("")
				manifest, packDir, err = reg.InstallFromRegistry(packFlag)
				if err != nil {
					return fmt.Errorf("pack %q not found locally or in registry: %w", packFlag, err)
				}
				installSource = "registry"
			} else {
				return fmt.Errorf("install pack: %w", err)
			}
		}
		fmt.Printf("Installed %s v%s\n", manifest.Name, manifest.Version)

		state, err := pack.LoadState(dir)
		if err != nil {
			state = &pack.PackState{}
		}

		applyMode := pack.ModeReplace
		if cfgExists == nil {
			applyMode = pack.ModeMerge
		}

		result, err := pack.Apply(dir, packDir, manifest, applyMode, state, pack.ApplyOpts{Source: installSource})
		if err != nil {
			return fmt.Errorf("apply pack: %w", err)
		}
		if err := state.Save(dir); err != nil {
			return fmt.Errorf("save pack state: %w", err)
		}

		if len(result.ConfigChanges) > 0 {
			fmt.Printf("Config updated: %v\n", result.ConfigChanges)
		}
		if len(result.OntologyAdded) > 0 {
			fmt.Printf("Ontology extended: %v\n", result.OntologyAdded)
		}
		if len(result.PromptsAdded) > 0 {
			fmt.Printf("Prompts added: %v\n", result.PromptsAdded)
		}
		if len(result.SamplesAdded) > 0 {
			fmt.Printf("Samples added: %v\n", result.SamplesAdded)
		}
	}

	// Generate agent skill file if requested
	if skillTarget != "" {
		target := skill.AgentTarget(skillTarget)
		info, err := skill.TargetInfoFor(target)
		if err != nil {
			return err
		}

		cfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("load config for skill generation: %w", err)
		}

		data := skill.BuildTemplateData(cfg)
		if err := skill.WriteSkill(dir, target, data); err != nil {
			return fmt.Errorf("write skill file: %w", err)
		}
		fmt.Printf("Agent skill written to %s\n", info.FileName)
	}

	fmt.Printf("\nProject %q initialized. Run: sage-wiki compile --watch\n", project)
	return nil
}

// reconcileStartup heals file<->DB drift once at process start (REL-03 / D5). It
// is best-effort: any failure is logged and startup continues. It is a no-op for
// an uninitialized project (no config or no database) and skips the expensive
// re-embed on already-consistent vaults (the reconciler's own fast paths).
func reconcileStartup(ctx context.Context, dir string) {
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return // not initialized — nothing to reconcile
	}
	dbPath := filepath.Join(dir, ".sage", "wiki.db")
	if _, err := os.Stat(dbPath); err != nil {
		return // no database yet
	}
	// P2-1 skip-list: no config in scope here; backend selection falls back
	// to the sqlite default (decisions.md 2026-07-21).
	db, err := storedial.OpenConcrete(dir, config.StorageConfig{})
	if err != nil {
		log.Warn("startup reconcile: open db failed", "error", err)
		return
	}
	defer db.Close()

	res, err := wiki.Reconcile(ctx, dir, cfg, db, embed.NewFromConfig(cfg))
	if err != nil {
		log.Warn("startup reconcile failed", "error", err)
		return
	}
	if res.Reindexed > 0 || res.Dropped > 0 {
		msg := fmt.Sprintf("reconcile: %d re-indexed, %d dropped", res.Reindexed, res.Dropped)
		if res.VectorsDeferred > 0 {
			msg += fmt.Sprintf(" (%d vectors deferred — embedder offline)", res.VectorsDeferred)
		}
		fmt.Fprintln(os.Stderr, msg)
	}
}

func runCompile(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	fresh, _ := cmd.Flags().GetBool("fresh")
	watch, _ := cmd.Flags().GetBool("watch")

	reEmbed, _ := cmd.Flags().GetBool("re-embed")
	if reEmbed {
		count, err := compiler.ReEmbed(dir)
		if err != nil {
			return err
		}
		fmt.Printf("Re-embedded %d entries.\n", count)
		return nil
	}

	reExtract, _ := cmd.Flags().GetBool("re-extract")
	if reExtract {
		result, err := compiler.ReExtract(dir)
		if err != nil {
			return err
		}
		fmt.Printf("Re-extract complete: %d concepts, %d articles, %d errors\n",
			result.ConceptsExtracted, result.ArticlesWritten, result.Errors)
		return nil
	}

	estimate, _ := cmd.Flags().GetBool("estimate")
	if estimate {
		return runEstimate(dir)
	}

	batch, _ := cmd.Flags().GetBool("batch")
	noCache, _ := cmd.Flags().GetBool("no-cache")
	prune, _ := cmd.Flags().GetBool("prune")

	if watch && batch {
		return fmt.Errorf("batch mode is incompatible with watch mode: batch compiles cannot be triggered by watch events")
	}

	// Cancellable compile: the first Ctrl-C / SIGTERM cancels gracefully (finishes
	// in-flight work, writes the checkpoint so the next run resumes); a second
	// forces an immediate exit.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nCancelling — finishing in-flight work; press Ctrl-C again to force quit.")
		cancel()
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nForce quit.")
		os.Exit(130)
	}()

	// Heal any file<->DB drift before compiling (REL-03 / D5). Runs before the
	// batch branching (which lives inside Compile) so `compile` and `compile
	// --batch` both reconcile. Skipped on a dry run, which must not mutate the DB.
	if !dryRun {
		reconcileStartup(ctx, dir)
	}

	if watch {
		fmt.Println("Watching for changes... (Ctrl+C to stop)")
		return compiler.Watch(dir, 2, compiler.CompileOpts{
			Ctx:     ctx,
			Fresh:   fresh,
			NoCache: noCache,
			Prune:   prune,
		})
	}

	// Interactive cost estimate prompt if config.compiler.estimate_before is true
	if err := maybePromptEstimate(dir); err != nil {
		return err
	}

	// P2-1 T9a-2: inject the Backend so compile honors storage.backend.
	// Error text matches Compile's own config-load failure byte-for-byte.
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return fmt.Errorf("compile: load config: %w", err)
	}
	backend, err := storedial.Open(cfg.Storage, store.OpenOptions{Mode: store.ModeWriter, ProjectDir: dir})
	if err != nil {
		return fmt.Errorf("compile: open db: %w", err)
	}
	defer backend.Close()

	result, err := compiler.Compile(dir, compiler.CompileOpts{
		Ctx:     ctx,
		DryRun:  dryRun,
		Fresh:   fresh,
		Batch:   batch,
		NoCache: noCache,
		Prune:   prune,
		Backend: backend,
	})
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, result, ""))
		return nil
	}

	fmt.Printf("Compile complete: +%d added, ~%d modified, -%d removed, %d summarized, %d concepts, %d articles",
		result.Added, result.Modified, result.Removed, result.Summarized,
		result.ConceptsExtracted, result.ArticlesWritten)
	if result.Errors > 0 {
		fmt.Printf(", %d errors", result.Errors)
	}
	fmt.Println()
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	// Heal any file<->DB drift once at server start, before either server is
	// built (D5). Doing it here — not inside mcp.NewServer and web.NewWebServer —
	// means `serve --ui` (which builds both) still reconciles exactly once.
	reconcileStartup(context.Background(), dir)

	// Web UI mode
	ui, _ := cmd.Flags().GetBool("ui")
	if ui {
		port, _ := cmd.Flags().GetInt("port")
		bind, _ := cmd.Flags().GetString("bind")

		webSrv, err := web.NewWebServer(dir)
		if err != nil {
			return err
		}
		defer webSrv.Close()

		// Resolve auth with precedence flag > env > config (NewWebServer already
		// applied the config value).
		token := webSrv.Token()
		if env := os.Getenv("SAGE_WIKI_TOKEN"); env != "" {
			token = env
		}
		if flagTok, _ := cmd.Flags().GetString("token"); flagTok != "" {
			token = flagTok
		}
		hosts := webSrv.AllowedHosts()
		if env := os.Getenv("SAGE_WIKI_ALLOWED_HOST"); env != "" {
			hosts = web.SplitHosts(env)
		}
		if flagHost, _ := cmd.Flags().GetString("allowed-host"); flagHost != "" {
			hosts = web.SplitHosts(flagHost)
		}
		webSrv.SetAuth(token, hosts)

		// Refuse to expose beyond loopback without a token (invariant: loopback
		// stays zero-config; anything wider must be authenticated).
		if err := web.CheckBindAuth(bind, token); err != nil {
			return err
		}
		if token != "" {
			fmt.Fprintln(os.Stderr, "🔐 token auth enabled on /api/* and /ws.")
		}
		if !web.IsLoopbackBind(bind) && len(hosts) == 0 {
			fmt.Fprintf(os.Stderr, "⚠️  Host allowlist is loopback-only — browsers reaching this by hostname/IP will be refused (403). Set --allowed-host <host> or SAGE_WIKI_ALLOWED_HOST.\n")
		}

		addr := net.JoinHostPort(bind, strconv.Itoa(port))

		// Graceful shutdown on SIGINT/SIGTERM.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return webSrv.Serve(ctx, addr)
	}

	// MCP server mode
	srv, err := mcppkg.NewServer(dir)
	if err != nil {
		return err
	}

	transport, _ := cmd.Flags().GetString("transport")
	if transport == "sse" {
		port, _ := cmd.Flags().GetInt("port")
		fmt.Fprintf(os.Stderr, "sage-wiki MCP server starting on SSE (127.0.0.1:%d)...\n", port)
		return srv.ServeSSE(port)
	}

	fmt.Fprintln(os.Stderr, "sage-wiki MCP server starting on stdio...")
	return srv.ServeStdio()
}

func runLint(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	fix, _ := cmd.Flags().GetBool("fix")
	passName, _ := cmd.Flags().GetString("pass")

	cfgPath := resolveConfigPath(dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ctx := &linter.LintContext{
		ProjectDir:       dir,
		OutputDir:        cfg.Output,
		DBPath:           filepath.Join(dir, ".sage", "wiki.db"),
		ValidRelations:   ontology.ValidRelationNames(mergedRels),
		ValidEntityTypes: ontology.ValidEntityTypeNames(mergedTypes),
		QualityThreshold: cfg.Compiler.QualityThreshold(),
	}

	runner := linter.NewRunner()
	results, err := runner.Run(ctx, passName, fix)
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	fmt.Print(linter.FormatFindings(results))

	if err := linter.SaveReport(dir, results); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save lint report: %v\n", err)
	}

	return nil
}

// P1-8: intentionally NOT adopted onto internal/app — runSearch tolerates
// config-load failure (BM25-only degrade) and honors the global --config
// flag via resolveConfigPath; both break under app.Open's strict shape.
func runSearch(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	queryStr := strings.Join(args, " ")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	limit, _ := cmd.Flags().GetInt("limit")

	// P2-1 skip-list: runSearch must tolerate config-load failure (P1-8
	// documented BM25 degrade); backend selection falls back to sqlite.
	db, err := storedial.OpenConcrete(dir, config.StorageConfig{})
	if err != nil {
		return err
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	searcher := hybrid.NewSearcher(memStore, vecStore)

	// Load config to get embed and search weight settings
	cfg, cfgErr := config.Load(resolveConfigPath(dir))

	var queryVec []float32
	if cfgErr == nil {
		embedder := embed.NewFromConfig(cfg)
		if embedder != nil {
			var embedErr error
			queryVec, embedErr = embedder.Embed(queryStr)
			if embedErr != nil {
				fmt.Fprintf(os.Stderr, "warning: embed failed, using BM25-only: %v\n", embedErr)
			}
		}
	}

	results, err := searcher.Search(hybrid.SearchOpts{
		Query: queryStr,
		Tags:  tags,
		Limit: limit,
	}, queryVec)
	if err != nil {
		return err
	}

	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, results, ""))
		return nil
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	for i, r := range results {
		fmt.Printf("%d. [%.4f] %s\n", i+1, r.RRFScore, r.ArticlePath)
		content := r.Content
		if len(content) > 120 {
			content = content[:120] + "..."
		}
		fmt.Printf("   %s\n", content)
		if len(r.Tags) > 0 {
			fmt.Printf("   tags: %s\n", strings.Join(r.Tags, ", "))
		}
		fmt.Println()
	}

	return nil
}

func runQuery(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	question := strings.Join(args, " ")

	result, err := query.Query(dir, question, "terminal", 5)
	if err != nil {
		return err
	}

	fmt.Println(result.Answer)
	if result.OutputPath != "" {
		fmt.Fprintf(os.Stderr, "\nFiled to: %s\n", result.OutputPath)
	}
	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	info, err := wiki.GetStatus(dir, nil)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	if outputFormat == "json" {
		fmt.Println(cli.FormatJSON(true, info, ""))
	} else {
		fmt.Print(wiki.FormatStatus(info))
	}
	return nil
}

func runIngest(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	target := args[0]

	var result *wiki.IngestResult
	var err error

	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		result, err = wiki.IngestURL(dir, target)
	} else {
		result, err = wiki.IngestPath(dir, target)
	}

	if err != nil {
		return err
	}

	fmt.Printf("Ingested: %s (type: %s, %d bytes)\n", result.SourcePath, result.Type, result.Size)
	fmt.Println("Run 'sage-wiki compile' to process.")
	return nil
}

func runDoctor(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	result := wiki.RunDoctor(dir)
	fmt.Print(wiki.FormatDoctor(result))
	if result.HasErrors() {
		return fmt.Errorf("doctor found errors")
	}
	return nil
}

// maybePromptEstimate shows a cost estimate and asks for confirmation
// if config.compiler.estimate_before is true.
func maybePromptEstimate(dir string) error {
	cfgPath := resolveConfigPath(dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil // non-fatal — compile will catch config errors
	}
	if !cfg.Compiler.EstimateBefore {
		return nil
	}

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return nil
	}

	diff, err := compiler.Diff(dir, cfg, mf)
	if err != nil {
		return nil
	}

	totalSources := len(diff.Added) + len(diff.Modified)
	if totalSources == 0 {
		return nil
	}

	var totalBytes int
	for _, s := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(dir, s.Path)
		info, err := os.Stat(absPath)
		if err == nil {
			totalBytes += int(info.Size())
		}
	}

	model := cfg.Models.Summarize
	if model == "" {
		model = "gemini-2.5-flash"
	}

	_, cost := llm.EstimateFromBytes(totalBytes, cfg.API.Provider, model, cfg.Compiler.TokenPriceOverride)

	fmt.Printf("Estimated: ~$%.4f for %d sources. Proceed? [y/n] ", cost, totalSources)
	var answer string
	fmt.Scanln(&answer)
	if answer != "y" && answer != "Y" && answer != "yes" {
		return fmt.Errorf("compilation cancelled by user")
	}
	return nil
}

func runEstimate(dir string) error {
	cfgPath := resolveConfigPath(dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return err
	}

	diff, err := compiler.Diff(dir, cfg, mf)
	if err != nil {
		return err
	}

	totalSources := len(diff.Added) + len(diff.Modified)
	if totalSources == 0 {
		fmt.Println("Nothing to compile — wiki is up to date.")
		return nil
	}

	// Count total bytes of source content
	var totalBytes int
	for _, s := range append(diff.Added, diff.Modified...) {
		absPath := filepath.Join(dir, s.Path)
		info, err := os.Stat(absPath)
		if err == nil {
			totalBytes += int(info.Size())
		}
	}

	model := cfg.Models.Summarize
	if model == "" {
		model = "gemini-2.5-flash"
	}

	tokens, cost := llm.EstimateFromBytes(totalBytes, cfg.API.Provider, model, cfg.Compiler.TokenPriceOverride)

	fmt.Printf("\n📊 Cost estimate for %d sources (%d new, %d modified)\n",
		totalSources, len(diff.Added), len(diff.Modified))
	fmt.Printf("   Model:    %s (%s)\n", model, cfg.API.Provider)
	fmt.Printf("   Tokens:   ~%d input (estimated)\n", tokens)
	fmt.Printf("   Cost:     ~$%.4f (standard mode)\n", cost)
	fmt.Printf("   Batch:    ~$%.4f (50%% discount, if available)\n", cost*0.5)
	fmt.Printf("   Cached:   ~$%.4f (with prompt caching)\n", cost*0.3)
	fmt.Println()
	fmt.Println("   Note: estimates are approximate. Actual cost depends on")
	fmt.Println("   content complexity, output length, and provider pricing.")
	fmt.Println()

	return nil
}

func runTUI(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	return tuidashboard.Run(dir)
}

func runProvenance(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	mfPath := filepath.Join(dir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return fmt.Errorf("provenance: load manifest: %w", err)
	}

	target := args[0]

	// Auto-detect: is it a source or a concept?
	if _, ok := mf.Sources[target]; ok {
		// Source → show articles
		articles := mf.ArticlesFromSource(target)
		if len(articles) == 0 {
			fmt.Printf("No articles generated from source: %s\n", target)
			return nil
		}
		fmt.Printf("Articles from source %s:\n", target)
		for _, name := range articles {
			c := mf.Concepts[name]
			fmt.Printf("  %s → %s\n", name, c.ArticlePath)
		}
		return nil
	}

	if c, ok := mf.Concepts[target]; ok {
		// Concept → show sources
		if len(c.Sources) == 0 {
			fmt.Printf("No sources for concept: %s\n", target)
			return nil
		}
		fmt.Printf("Sources for concept %s:\n", target)
		for _, s := range c.Sources {
			fmt.Printf("  %s\n", s)
		}
		return nil
	}

	return fmt.Errorf("provenance: %q not found in sources or concepts. Use a source path (e.g. raw/paper.pdf) or concept name (e.g. attention)", target)
}

func runScribe(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	filePath := args[0]

	// Load config
	cfgPath := resolveConfigPath(dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("scribe: load config: %w", err)
	}

	// Read session file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("scribe: read file: %w", err)
	}

	// Create LLM client
	client, err := auth.NewLLMClient(cfg)
	if err != nil {
		return fmt.Errorf("scribe: create LLM client: %w", err)
	}

	// Open DB
	db, err := storedial.OpenConcrete(dir, cfg.Storage)
	if err != nil {
		return fmt.Errorf("scribe: open db: %w", err)
	}
	defer db.Close()

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes))

	model := cfg.Models.Extract
	if model == "" {
		model = cfg.Models.Summarize
	}

	// Run session scribe with configured entity types
	validTypes := ontology.ValidEntityTypeNames(mergedTypes)
	s := scribe.NewSessionScribe(client, model, ontStore, validTypes...)
	result, err := s.Process(cmd.Context(), data)
	if err != nil {
		return fmt.Errorf("scribe: %w", err)
	}

	// Report
	fmt.Fprintf(os.Stderr, "Session scribe complete:\n")
	fmt.Fprintf(os.Stderr, "  Input: %d bytes → %d bytes compressed\n", result.InputSize, result.CompressedTo)
	fmt.Fprintf(os.Stderr, "  Extracted: %d candidates\n", result.Extracted)
	fmt.Fprintf(os.Stderr, "  Kept: %d entities, %d skipped\n", result.Kept, result.Skipped)

	for _, e := range result.Entities {
		fmt.Printf("  + %s (%s): %s\n", e.Name, e.Type, e.Definition)
	}
	for _, r := range result.Relations {
		fmt.Printf("  → %s -[%s]→ %s\n", r.SourceID, r.Relation, r.TargetID)
	}

	return nil
}
