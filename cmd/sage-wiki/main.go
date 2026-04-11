package main

import (
	"fmt"
	"os"

	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/log"
	"strings"

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
	"github.com/xoai/sage-wiki/internal/prompts"
	tuidashboard "github.com/xoai/sage-wiki/internal/tui/dashboard"
	"github.com/xoai/sage-wiki/internal/web"
	"github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
)

var (
	projectDir string
	configPath string
	verbosity  int
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
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

func init() {
	rootCmd.PersistentFlags().StringVar(&projectDir, "project", ".", "Project directory")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Config file path (default: <project>/config.yaml)")
	rootCmd.PersistentFlags().CountVarP(&verbosity, "verbose", "v", "Increase log verbosity (-v for info, -vv for debug)")

	// Init flags
	initCmd.Flags().Bool("vault", false, "Initialize as vault overlay on existing Obsidian vault")
	initCmd.Flags().Bool("prompts", false, "Scaffold prompt templates for customization")
	initCmd.Flags().String("model", "gemini-2.5-flash", "Default LLM model for all tasks (e.g. gemini-2.5-flash, gemini-3.1-flash-lite)")

	// Compile flags
	compileCmd.Flags().Bool("watch", false, "Watch for changes and recompile")
	compileCmd.Flags().Bool("dry-run", false, "Show what would change without writing")
	compileCmd.Flags().Bool("fresh", false, "Ignore checkpoint, clean compile")
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

	// Lint flags
	lintCmd.Flags().Bool("fix", false, "Auto-fix issues")
	lintCmd.Flags().String("pass", "", "Run specific lint pass")
	lintCmd.Flags().Bool("dry-run", false, "Show findings without fixing")

	// Search flags
	searchCmd.Flags().StringSlice("tags", nil, "Filter by tags")
	searchCmd.Flags().Int("limit", 10, "Maximum results")

	rootCmd.AddCommand(initCmd, compileCmd, serveCmd, lintCmd, searchCmd, queryCmd, statusCmd, ingestCmd, doctorCmd, tuiCmd, provenanceCmd)
}

// Placeholder implementations — will be filled in subsequent tasks

func runInit(cmd *cobra.Command, args []string) error {
	vaultMode, _ := cmd.Flags().GetBool("vault")
	model, _ := cmd.Flags().GetString("model")
	dir, _ := filepath.Abs(projectDir)

	// Derive project name from directory
	project := filepath.Base(dir)

	if vaultMode {
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

	fmt.Printf("\nProject %q initialized. Run: sage-wiki compile --watch\n", project)
	return nil
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

	if watch {
		fmt.Println("Watching for changes... (Ctrl+C to stop)")
		return compiler.Watch(dir, 2)
	}

	batch, _ := cmd.Flags().GetBool("batch")
	noCache, _ := cmd.Flags().GetBool("no-cache")
	prune, _ := cmd.Flags().GetBool("prune")

	// Interactive cost estimate prompt if config.compiler.estimate_before is true
	if err := maybePromptEstimate(dir); err != nil {
		return err
	}

	result, err := compiler.Compile(dir, compiler.CompileOpts{
		DryRun:  dryRun,
		Fresh:   fresh,
		Batch:   batch,
		NoCache: noCache,
		Prune:   prune,
	})
	if err != nil {
		return err
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

	// Web UI mode
	ui, _ := cmd.Flags().GetBool("ui")
	if ui {
		port, _ := cmd.Flags().GetInt("port")
		bind, _ := cmd.Flags().GetString("bind")

		if bind != "127.0.0.1" && bind != "localhost" {
			fmt.Fprintf(os.Stderr, "⚠️  WARNING: binding to %s exposes the wiki on the network. No authentication is enabled.\n\n", bind)
		}

		webSrv, err := web.NewWebServer(dir)
		if err != nil {
			return err
		}
		defer webSrv.Close()

		addr := fmt.Sprintf("%s:%d", bind, port)
		return webSrv.Start(addr)
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

	cfgPath := filepath.Join(dir, "config.yaml")
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
	}

	runner := linter.NewRunner()
	results, err := runner.Run(ctx, passName, fix)
	if err != nil {
		return err
	}

	fmt.Print(linter.FormatFindings(results))

	if err := linter.SaveReport(dir, results); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save lint report: %v\n", err)
	}

	return nil
}

func runSearch(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	queryStr := strings.Join(args, " ")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	limit, _ := cmd.Flags().GetInt("limit")

	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	searcher := hybrid.NewSearcher(memStore, vecStore)

	var queryVec []float32
	cfg, cfgErr := config.Load(filepath.Join(dir, "config.yaml"))
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
		return err
	}
	fmt.Print(wiki.FormatStatus(info))
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
	cfgPath := filepath.Join(dir, "config.yaml")
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
	cfgPath := filepath.Join(dir, "config.yaml")
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
