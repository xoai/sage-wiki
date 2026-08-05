package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/api"
	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/manifest"
	mcppkg "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/pack"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/query"
	"github.com/xoai/sage-wiki/internal/scribe"
	"github.com/xoai/sage-wiki/internal/serve"
	"github.com/xoai/sage-wiki/internal/skill"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/trust"
	tuidashboard "github.com/xoai/sage-wiki/internal/tui/dashboard"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/web"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
	"github.com/xoai/sage-wiki/pkg/events"
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

	if err := executeWithShipPass(); err != nil {
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
	Use:   "init [dir]",
	Short: "Initialize a new sage-wiki project",
	Args:  cobra.MaximumNArgs(1),
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
	initCmd.Flags().Bool("force", false, "Rewrite .gitignore and .manifest.json even when they exist (config.yaml is always preserved)")
	initCmd.Flags().Bool("prompts", false, "Scaffold prompt templates for customization")
	initCmd.Flags().String("model", "gemini-2.5-flash", "Default LLM model for all tasks (e.g. gemini-2.5-flash, gemini-3.1-flash-lite)")
	initCmd.Flags().String("skill", "", "Generate agent skill file (claude-code, cursor, windsurf, agents-md, codex, gemini, generic)")
	initCmd.Flags().String("pack", "", "Install a contribution pack during init")

	// Compile flags
	compileCmd.Flags().Bool("upgrade", false, "Adopt a pre-format (v0.2.x) workspace (one-way) and compile")
	compileCmd.Flags().Bool("watch", false, "Watch for changes and recompile")
	compileCmd.Flags().Bool("dry-run", false, "Show what would change without writing")
	compileCmd.Flags().Bool("fresh", false, "Clear checkpoint state (batch + legacy) and recompile from scratch")
	compileCmd.Flags().Bool("re-embed", false, "Re-generate embeddings for all entries without recompiling")
	compileCmd.Flags().Bool("re-extract", false, "Re-run concept extraction and article writing from existing summaries")
	compileCmd.Flags().Bool("estimate", false, "Show cost estimate without compiling")
	compileCmd.Flags().Bool("batch", false, "Use batch API for 50% cost reduction (async)")
	compileCmd.Flags().Bool("no-cache", false, "Disable prompt caching for this run")
	compileCmd.Flags().Bool("prune", false, "Delete orphaned articles when their sole source is removed")
	compileCmd.Flags().Bool("force", false, "Recompile every doc regardless of compile keys (SPEC-04)")
	compileCmd.Flags().String("explain", "", "Print the compile-key inputs for one doc (why it would compile or skip) and exit")

	// Reindex flags
	reindexCmd.Flags().Bool("drop-chunk-vectors", false, "Rebuild the text index without an embedder — chunk vectors are deleted, not rebuilt")
	rebuildVectorsCmd.Flags().String("quantize", "", "Index quantization: none (default, from vectors.quantization) or int8")
	rebuildVectorsCmd.Flags().Bool("upgrade", false, "Adopt a pre-format (v0.2.x) workspace before rebuilding")
	indexCmd.AddCommand(rebuildVectorsCmd)

	// Serve flags
	serveCmd.Flags().String("transport", "stdio", "Transport: stdio or sse")
	serveCmd.Flags().String("addr", "", "HTTP mode bind (REST+MCP+metrics, takes the workspace lock; bare serve defaults to 127.0.0.1:8484)")
	serveCmd.Flags().String("workspace", "", "workspace dir for HTTP mode (default: --project)")
	serveCmd.Flags().String("workspace-root", "", "multi-workspace mode (SPEC-06): serve every workspace under this root at /w/{name}/ (HTTP only; incompatible with --workspace, --ui, --transport)")
	serveCmd.Flags().Int("max-open", 0, "multi-workspace: max workspaces held open (LRU closes beyond this; 0 = unlimited)")
	serveCmd.Flags().Duration("idle-close", 0, "multi-workspace: close workspaces idle longer than this (e.g. 10m; 0 = off)")
	serveCmd.Flags().String("token-file", "", "bearer token file for HTTP mode (one per line)")
	serveCmd.Flags().Int("max-concurrent-compiles", 2, "global cap on concurrent compiles (matters for SPEC-06 multi-workspace; single-workspace FIFO already serializes)")
	serveCmd.Flags().Duration("drain-timeout", 30*time.Second, "graceful shutdown drain budget (min 10s, warns when clamped)")
	serveCmd.Flags().Bool("insecure-no-auth", false, "allow non-loopback bind without tokens in HTTP mode")
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
	searchCmd.Flags().StringSlice("boost-tags", nil, "Rank documents carrying these tags higher (+3% each, cap 15%) without excluding others")
	searchCmd.Flags().Int("limit", 10, "Maximum results")
	searchCmd.Flags().String("channels", "", "Comma-separated channel subset: bm25, vector, graph (default: all)")
	searchCmd.Flags().Bool("expand", false, "LLM query expansion (default off)")
	searchCmd.Flags().Bool("rerank", false, "LLM reranking (default off)")

	// Query flags
	queryCmd.Flags().String("scope", "local", "Query scope: local, global, or all")
	queryCmd.Flags().Bool("upgrade", false, "Adopt a pre-format (v0.2.x) workspace (one-way)")
	ingestCmd.Flags().Bool("upgrade", false, "Adopt a pre-format (v0.2.x) workspace (one-way)")

	rootCmd.AddCommand(initCmd, compileCmd, reindexCmd, indexCmd, serveCmd, lintCmd, searchCmd, queryCmd, statusCmd, ingestCmd, doctorCmd, tuiCmd, provenanceCmd, scribeCmd, diffCmd, listCmd, ontologyCmd, writeCmd, learnCmd, captureCmd, addSourceCmd, sourceCmd, hubCmd, skillCmd, packCmd, costCmd, versionCmd, mirrorCmd)

	// Enables `sage-wiki --version` in addition to the `version` subcommand.
	rootCmd.Version = version

	// Report the real build version to MCP clients on initialize, instead of
	// the constant the server package defaults to.
	mcppkg.Version = version

	// Stamp new workspace manifests with the real build version (SPEC-01).
	manifest.EngineVersion = version
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
	force, _ := cmd.Flags().GetBool("force")
	// The positional arg is an alias for --project (#127): without this,
	// `init <dir>` silently initialized the CURRENT directory — and could
	// wipe the wrong vault's manifest before preservation existed.
	if len(args) == 1 {
		if projectDir != "." && projectDir != "" && projectDir != args[0] {
			fmt.Fprintf(os.Stderr, "note: positional dir %q overrides --project %q\n", args[0], projectDir)
		}
		projectDir = args[0]
	}
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

		if err := wiki.InitVaultOverlay(dir, project, sourceFolders, ignoreFolders, "_wiki", model, wiki.WithForce(force)); err != nil {
			return err
		}
	} else {
		if err := wiki.InitGreenfield(dir, project, model, wiki.WithForce(force)); err != nil {
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
	// P3-7: uniform nothing-compiled guard. InitGreenfield writes an EMPTY
	// manifest, so presence alone is not enough — skip when the manifest is
	// absent OR has no sources and no concepts. Without this a sqlite writer
	// open creates a stray wiki.db on every startup of a never-compiled
	// vault, and a PG writer open takes the advisory lock for nothing.
	mf, mfErr := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if mfErr != nil {
		if !os.IsNotExist(mfErr) {
			// A corrupt manifest on a compiled vault is worth a line — the
			// old path warned "load manifest"; absent is the intended skip.
			log.Warn("startup reconcile: manifest unreadable, skipping", "error", mfErr)
		}
		return
	}
	if len(mf.Sources) == 0 && len(mf.Concepts) == 0 {
		return
	}
	// Backend selection honors storage.backend (P3-7 — the P2-1 skip-list
	// entry for this site is retired). ModeWriter is required: reconcile
	// writes. On PG, a contended writer open stalls up to the configured
	// lock_timeout then fails — the warn below converts that to a skipped
	// reconcile, never a blocked startup.
	backend, err := storedial.OpenWithConfig(cfg, dir, store.ModeWriter)
	if err != nil {
		log.Warn("startup reconcile: open backend failed", "error", err)
		return
	}
	defer backend.Close()

	res, err := wiki.ReconcileBackend(ctx, dir, cfg, backend, embed.NewFromConfig(cfg))
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
	defer metrics.LogSnapshot() // P2-2: one-shot CLI metrics are never lost
	dir, _ := filepath.Abs(projectDir)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	fresh, _ := cmd.Flags().GetBool("fresh")
	watch, _ := cmd.Flags().GetBool("watch")

	reEmbed, _ := cmd.Flags().GetBool("re-embed")
	if reEmbed {
		// P2-1: inject the Backend so re-embed honors storage.backend.
		cfg, err := config.Load(resolveConfigPath(dir))
		if err != nil {
			return fmt.Errorf("re-embed: load config: %w", err)
		}
		backend, err := storedial.Open(cfg.Storage, store.OpenOptions{Mode: store.ModeWriter, ProjectDir: dir, TemporalEnabled: cfg.Ontology.Temporal.Enabled, Now: config.NowUTC})
		if err != nil {
			return fmt.Errorf("re-embed: open db: %w", err)
		}
		defer backend.Close()
		count, err := compiler.ReEmbed(dir, backend)
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

	// SPEC-01 carve-outs on internal wiring: --re-embed, --re-extract, and
	// watch mode (above) have no engine surface; reconcileStartup,
	// maybePromptEstimate, the SIGINT handler, and metrics.LogSnapshot stay
	// in this shim (CLI/process concerns, not engine behavior).
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

	// Routed through pkg/engine (SPEC-01): the workspace lock, format
	// adoption, and store wiring are the engine's; storage.backend is
	// honored via the engine's storedial open (same options as before).
	upgrade, _ := cmd.Flags().GetBool("upgrade")
	var openOpts []engine.Option
	if upgrade {
		openOpts = append(openOpts, engine.WithUpgrade())
	}
	evOpts, evClose := cliEventPlane(ctx, dir)
	defer evClose()
	openOpts = append(openOpts, evOpts...)
	w, err := engine.Open(ctx, dir, openOpts...)
	if err != nil {
		return cli.CLIError(outputFormat, err)
	}
	defer w.Close()
	// Dry-run mutates nothing, so it may run against a pre-format workspace
	// without the one-way adoption (B-04).
	if w.RequiresUpgrade() && !dryRun {
		return cli.CLIError(outputFormat, fmt.Errorf("workspace predates format versioning (v0.2.x) — re-run with --upgrade to adopt it (one-way); reads still work"))
	}

	explain, _ := cmd.Flags().GetString("explain")
	if explain != "" {
		ex, err := w.ExplainCompile(ctx, explain)
		if err != nil {
			return cli.CLIError(outputFormat, err)
		}
		force, _ := cmd.Flags().GetBool("force")
		if force && ex.Verdict != "compile: incomplete (resume)" && ex.Verdict != "compile: content" && ex.Verdict != "compile: content (new)" {
			// R0 wins over force for interrupted docs, and content-changed/new
			// docs keep their content verdicts (the real compile's attribution).
			ex.Verdict = "compile: forced"
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, ex, ""))
			return nil
		}
		fmt.Printf("Doc:          %s\n", ex.Path)
		fmt.Printf("Source hash:  %s\n", ex.SourceHash)
		fmt.Printf("Pipeline:     %s\n", ex.Pipeline)
		fmt.Printf("Templates:    %s\n", ex.Templates)
		fmt.Printf("Models:       %s\n", ex.Models)
		fmt.Printf("Config hash:  %s\n", ex.ConfigHash)
		fmt.Printf("Embed:        %s\n", ex.Embed)
		fmt.Printf("Key:          %s\n", ex.Key)
		fmt.Printf("Stored key:   %s\n", ex.StoredKey)
		printPartsDiff(ex)
		fmt.Printf("Verdict:      %s\n", ex.Verdict)
		return nil
	}

	force, _ := cmd.Flags().GetBool("force")
	result, err := w.Compile(ctx, engine.CompileRequest{
		Selector: "pending",
		Tier:     engine.TierUseConfig,
		DryRun:   dryRun,
		Fresh:    fresh,
		Force:    force,
		Batch:    batch,
		NoCache:  noCache,
		Prune:    prune,
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
	unchanged := 0
	for _, s := range result.Skipped {
		if s.Reason == "unchanged" {
			unchanged++
		}
	}
	if unchanged > 0 || result.Adopted > 0 {
		fmt.Printf(", %d unchanged (skipped), %d keys adopted", unchanged, result.Adopted)
	}
	if result.Errors > 0 {
		fmt.Printf(", %d errors", result.Errors)
	}
	fmt.Println()
	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)

	// Multi-workspace mode (SPEC-06): --workspace-root is HTTP-only and
	// mutually exclusive with the single-workspace surfaces — every
	// exclusion is an explicit startup error, never a silent ignore.
	wsRoot, _ := cmd.Flags().GetString("workspace-root")
	if wsRoot != "" {
		if cmd.Flags().Changed("transport") {
			return fmt.Errorf("serve: --workspace-root runs its own HTTP server — it cannot be combined with --transport (stdio/sse are single-workspace transports)")
		}
		if uiFlag, _ := cmd.Flags().GetBool("ui"); uiFlag {
			return fmt.Errorf("serve: --workspace-root cannot be combined with --ui (the web UI is a per-workspace surface)")
		}
		if cmd.Flags().Changed("workspace") {
			return fmt.Errorf("serve: --workspace-root and --workspace are mutually exclusive")
		}
		addr, _ := cmd.Flags().GetString("addr")
		if addr == "" {
			addr = "127.0.0.1:8484"
		}
		return runServeMulti(cmd, wsRoot, addr)
	}

	// Heal any file<->DB drift once at server start, before either server is
	// built (D5). Doing it here — not inside mcp.NewServer and web.NewWebServer —
	// means `serve --ui` (which builds both) still reconciles exactly once.
	reconcileStartup(context.Background(), dir)

	// HTTP mode (SPEC-02): --addr set, OR bare `serve` with no --transport
	// and no --ui. Takes the workspace lock; REST + streamable MCP +
	// metrics on one listener. --transport stdio|sse and --ui keep the
	// pre-existing lock-free behavior.
	addr, _ := cmd.Flags().GetString("addr")
	uiEarly, _ := cmd.Flags().GetBool("ui")
	if useHTTPMode(addr, cmd.Flags().Changed("transport"), uiEarly) {
		if addr == "" {
			addr = "127.0.0.1:8484"
		}
		return runServeHTTP(cmd, dir, addr)
	}

	// Shared serve-mode compile state (P2-3): one coordinator + one progress
	// hub + the queue worker (nil when serve.worker.enabled: false).
	deps, err := serve.AssembleDeps(dir)
	if err != nil {
		return err
	}
	defer deps.Close()

	// SPEC-07: the long-lived non-HTTP surfaces (web UI, stdio/sse MCP)
	// join the event plane too — worker compiles, mirror passes, and
	// on-demand compiles emit into the same audit trail.
	var planeBus *events.Bus
	if planeCfg, cfgErr := config.Load(resolveConfigPath(dir)); cfgErr == nil {
		var planeStops []func()
		planeBus, planeStops, err = serve.BuildEventSurfaces(cmd.Context(), dir, planeCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: webhooks unavailable — audit trail only: %v\n", err)
		}
		if planeBus != nil {
			deps.SetEventSink(planeBus)
			defer func() {
				planeBus.Close() // drain first, then stop the dispatchers
				for _, stop := range planeStops {
					stop()
				}
			}()
		}
	}

	// Web UI mode
	ui, _ := cmd.Flags().GetBool("ui")
	if ui {
		port, _ := cmd.Flags().GetInt("port")
		bind, _ := cmd.Flags().GetString("bind")

		webSrv, err := web.NewWebServer(dir, deps.Progress())
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

		// Public REST facade (P4-1): an MCP server sharing the serve-mode
		// coordinator (compile serialization), mounted on the web mux inside
		// the existing security middleware.
		mcpSrv, err := mcppkg.NewServer(dir, deps.Coordinator())
		if err != nil {
			return err
		}
		mcpSrv.SetEventSink(planeBus) // SPEC-07
		defer mcpSrv.Close()
		webSrv.SetV1Handler(api.New(mcpSrv, webSrv.Config(), dir, serve.NewJobRunner(deps, mcpSrv), deps.Progress()).Handler())

		// Refuse to expose beyond loopback without a token (invariant: loopback
		// stays zero-config; anything wider must be authenticated).
		if err := web.CheckBindAuth(bind, token); err != nil {
			return err
		}
		if token != "" {
			fmt.Fprintln(os.Stderr, "🔐 token auth enabled on /api/*, /v1/* and /ws.")
		}
		if !web.IsLoopbackBind(bind) && len(hosts) == 0 {
			fmt.Fprintf(os.Stderr, "⚠️  Host allowlist is loopback-only — browsers reaching this by hostname/IP will be refused (403). Set --allowed-host <host> or SAGE_WIKI_ALLOWED_HOST.\n")
		}

		addr := net.JoinHostPort(bind, strconv.Itoa(port))

		// Graceful shutdown on SIGINT/SIGTERM.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if deps.WorkerEnabled() {
			deps.StartWorker(ctx)
			fmt.Fprintln(os.Stderr, "⚙️  compile worker started (serve.worker).")
		}
		deps.StartMirror(ctx)
		return webSrv.Serve(ctx, addr)
	}

	// MCP server mode
	srv, err := mcppkg.NewServer(dir, deps.Coordinator())
	if err == nil {
		srv.SetEventSink(planeBus) // SPEC-07
	}
	if err != nil {
		return err
	}
	defer srv.Close() // also fires the P2-2 shutdown snapshot (D8)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if deps.WorkerEnabled() {
		deps.StartWorker(ctx)
		fmt.Fprintln(os.Stderr, "⚙️  compile worker started (serve.worker).")
	}
	deps.StartMirror(ctx)

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
		TemporalEnabled:  cfg.Ontology.Temporal.Enabled,
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

// cliSearchOpts builds the hybrid search options for the CLI path. A nil
// cfg (config-load failure) leaves the weights zero so hybrid.Search
// applies its own defaults — the documented degrade, unchanged.
func cliSearchOpts(cfg *config.Config, query string, tags []string, limit int) hybrid.SearchOpts {
	opts := hybrid.SearchOpts{
		Query: query,
		Tags:  tags,
		Limit: limit,
	}
	if cfg != nil {
		opts.BM25Weight = cfg.Search.HybridWeightBM25
		opts.VectorWeight = cfg.Search.HybridWeightVector
	}
	return opts
}

// P1-8: intentionally NOT adopted onto internal/app — runSearch tolerates
// config-load failure (BM25-only degrade) and honors the global --config
// flag via resolveConfigPath; both break under app.Open's strict shape.
func runSearch(cmd *cobra.Command, args []string) error {
	defer metrics.LogSnapshot() // P2-2: one-shot CLI metrics are never lost
	dir, _ := filepath.Abs(projectDir)
	queryStr := strings.Join(args, " ")
	tags, _ := cmd.Flags().GetStringSlice("tags")
	boostTags, _ := cmd.Flags().GetStringSlice("boost-tags")
	limit, _ := cmd.Flags().GetInt("limit")

	// P2-1 skip-list: runSearch must tolerate config-load failure (P1-8
	// documented BM25 degrade); backend selection falls back to sqlite.
	db, err := storedial.OpenConcrete(dir, config.StorageConfig{})
	if err != nil {
		return err
	}
	defer db.Close()

	// Config first: store construction needs the ANN setting, and the
	// searcher needs the hybrid weights. Auto-discovery load failure
	// degrades to BM25-only with hybrid's own weight defaults; an
	// EXPLICIT --config that fails to load is a hard error — exit 0
	// would mask a typo'd path (F-043).
	cfg, cfgErr := config.Load(resolveConfigPath(dir))
	if cfgErr != nil {
		if configPath != "" {
			return fmt.Errorf("explicit --config failed to load: %w", cfgErr)
		}
		fmt.Fprintf(os.Stderr, "warning: config load failed (%v): default fusion weights, ANN off, BM25-only\n", cfgErr)
	}

	memStore := memory.NewStore(db)
	var vecStore *vectors.Store
	if cfgErr == nil {
		vecStore = vectors.NewStore(db, vectors.WithANN(cfg.Search.ANNEnabled()), vectors.WithVectorBackend(cfg.VectorBackend()), vectors.WithIndexDir(filepath.Join(dir, ".sage")))
		if cfg.Search.ANNEnabled() {
			log.Debug("vector search: ANN (HNSW) index enabled") // F-044 observability
		}
	} else {
		vecStore = vectors.NewStore(db)
	}

	var embedder embed.Embedder
	if cfgErr == nil {
		embedder = embed.NewFromConfig(cfg)
	}

	// Unified pipeline (ADR-036, M5) unless config pins legacy or the
	// config failed to load (legacy is the only path without a config).
	// SPEC-01: the unified path is routed through pkg/engine — a read-only
	// Open (search is lock-free today; spec §B.2.2). The db above stays
	// open for the legacy/degrade branch; the engine opens its own handles.
	if cfgErr == nil && cfg.Search.PipelineOrDefault() == "unified" {
		channelsFlag, _ := cmd.Flags().GetString("channels")
		expand, _ := cmd.Flags().GetBool("expand")
		rerank, _ := cmd.Flags().GetBool("rerank")
		var channels []string
		if channelsFlag != "" {
			raw := strings.Split(channelsFlag, ",")
			for _, c := range raw {
				if c = strings.TrimSpace(c); c != "" {
					channels = append(channels, c)
				}
			}
		}

		// P1-8 degrade, engine flavor (F-018/F-026): ONLY a config-load
		// failure with no explicit --config falls back to the legacy path.
		// SPEC-07: searches join the audit trail too (read-only open still
		// emits search_performed).
		evOpts, evClose := cliEventPlane(cmd.Context(), dir)
		defer evClose()
		w, err := engine.Open(cmd.Context(), dir, append([]engine.Option{engine.WithReadOnly(), engine.WithConfigFile(resolveConfigPath(dir))}, evOpts...)...)
		if err != nil {
			if searchFallsBackToLegacy(err, configPath) {
				fmt.Fprintf(os.Stderr, "warning: config load failed (%v): default fusion weights, ANN off, BM25-only\n", err)
				goto legacy
			}
			return err
		}
		defer w.Close()

		res, err := w.Search(cmd.Context(), engine.SearchRequest{
			Query:      queryStr,
			Limit:      limit,
			Channels:   channels,
			Expand:     expand,
			Rerank:     rerank,
			FilterTags: tags,
			Tags:       boostTags,
		})
		if err != nil {
			return err
		}
		if outputFormat == "json" {
			fmt.Println(cli.FormatJSON(true, res.Results, ""))
			return nil
		}
		if len(res.Results) == 0 {
			fmt.Println("No results found.")
			return nil
		}
		for i, r := range res.Results {
			fmt.Printf("%d. [%.4f] %s\n", i+1, r.Score, r.ArticlePath)
			content := r.Text
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

legacy:

	// Legacy doc-level path (config pin or config-load degrade).
	searcher := hybrid.NewSearcher(memStore, vecStore)

	var queryVec []float32
	if embedder != nil {
		var embedErr error
		queryVec, embedErr = embedder.Embed(queryStr)
		if embedErr != nil {
			fmt.Fprintf(os.Stderr, "warning: embed failed, using BM25-only: %v\n", embedErr)
		}
	}

	var optsCfg *config.Config
	if cfgErr == nil {
		optsCfg = cfg
	}
	results, err := searcher.Search(cliSearchOpts(optsCfg, queryStr, tags, limit), queryVec)
	if err != nil {
		return err
	}

	// Trust filtering is not part of the pipeline rollback: `pipeline:
	// legacy` (or a config-load degrade) must not re-admit unverified
	// `output:` docs. With no loadable config the conservative "false"
	// mode applies, which is what an unconfigured project would get.
	legacyMode := "false"
	var legacyTrust *trust.Store
	if cfgErr == nil {
		legacyMode = cfg.Trust.IncludeOutputsMode()
		if legacyMode == "verified" {
			legacyTrust = trust.NewStore(db)
		}
	}
	legacyInclude := trust.IncludePredicate(legacyMode, legacyTrust)
	kept := results[:0]
	for _, r := range results {
		if legacyInclude(r.ID) {
			kept = append(kept, r)
		}
	}
	results = kept

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
	defer metrics.LogSnapshot() // P2-2: one-shot CLI metrics are never lost
	dir, _ := filepath.Abs(projectDir)
	question := strings.Join(args, " ")

	// SPEC-01: query auto-files its answer (a workspace mutation), so it
	// takes the engine's single-writer lock and fails fast during an
	// active compile (spec §B.1 8e). The Q&A pipeline itself has no
	// engine surface (SPEC-01 exposes no Query method) — the Open is the
	// lock, the pipeline is unchanged.
	upgrade, _ := cmd.Flags().GetBool("upgrade")
	var openOpts []engine.Option
	if upgrade {
		openOpts = append(openOpts, engine.WithUpgrade())
	}
	evOpts, evClose := cliEventPlane(cmd.Context(), dir)
	defer evClose()
	openOpts = append(openOpts, evOpts...)
	w, err := engine.Open(cmd.Context(), dir, openOpts...)
	if err != nil {
		return cli.CLIError(outputFormat, lockSentinel(err))
	}
	defer w.Close()
	if w.RequiresUpgrade() {
		return cli.CLIError(outputFormat, fmt.Errorf("workspace predates format versioning (v0.2.x) — re-run with --upgrade to adopt it (one-way)"))
	}

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

// searchFallsBackToLegacy is the P1-8 degrade decision, extracted for
// testing: ONLY an engine config-load failure with NO explicit --config
// drops search to the legacy BM25 path; every other Open error (including
// an explicit --config failure) propagates.
func searchFallsBackToLegacy(err error, explicitConfig string) bool {
	return errors.Is(err, engine.ErrConfigLoad) && explicitConfig == ""
}

// lockSentinel maps the engine's lock failure onto the CLI surface
// specified in spec §B.1 8e: exit 1 (cobra RunE) with this message text.
func lockSentinel(err error) error {
	if errors.Is(err, engine.ErrLocked) {
		return fmt.Errorf("workspace is locked by another process (compile in progress?)")
	}
	return err
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

	// SPEC-01: ingest registers the source in the manifest (a mutation) —
	// take the engine's single-writer lock like capture/query (F-048).
	upgrade, _ := cmd.Flags().GetBool("upgrade")
	var openOpts []engine.Option
	if upgrade {
		openOpts = append(openOpts, engine.WithUpgrade())
	}
	evOpts, evClose := cliEventPlane(cmd.Context(), dir)
	defer evClose()
	openOpts = append(openOpts, evOpts...)
	w, err := engine.Open(cmd.Context(), dir, openOpts...)
	if err != nil {
		return cli.CLIError(outputFormat, lockSentinel(err))
	}
	defer w.Close()
	if w.RequiresUpgrade() {
		return cli.CLIError(outputFormat, fmt.Errorf("workspace predates format versioning (v0.2.x) — re-run with --upgrade to adopt it (one-way)"))
	}

	var result *wiki.IngestResult

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

	_, cost, err := llm.EstimateFromBytes(totalBytes, cfg.API.Provider, model, cfg.Compiler.TokenPriceOverride, cfg.Compiler.PriceTable)
	if err != nil {
		return fmt.Errorf("cost estimate: %w", err)
	}

	if cost == nil {
		fmt.Printf("Estimated: unknown (model %q not in price registry) for %d sources. Proceed? [y/n] ", model, totalSources)
	} else {
		fmt.Printf("Estimated: ~$%s for %d sources. Proceed? [y/n] ", cost.StringFixed(4), totalSources)
	}
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

	variants, err := llm.EstimateVariantsFromBytes(totalBytes, cfg.API.Provider, model, cfg.Compiler.TokenPriceOverride, cfg.Compiler.PriceTable)
	if err != nil {
		return fmt.Errorf("cost estimate: %w", err)
	}
	tokens, cost := variants.InputTokens, variants.Standard

	fmt.Printf("\n📊 Cost estimate for %d sources (%d new, %d modified)\n",
		totalSources, len(diff.Added), len(diff.Modified))
	fmt.Printf("   Model:    %s (%s)\n", model, cfg.API.Provider)
	fmt.Printf("   Tokens:   ~%d input (estimated)\n", tokens)
	if cost == nil {
		fmt.Printf("   Cost:     unknown (model not in price registry)\n")
	} else {
		fmt.Printf("   Cost:     ~$%s (standard mode)\n", cost.StringFixed(4))
		if variants.Batch != nil {
			fmt.Printf("   Batch:    ~$%s (registry batch rates)\n", variants.Batch.StringFixed(4))
		}
		if variants.Cached != nil {
			fmt.Printf("   Cached:   ~$%s (registry cached-input rate, best case)\n", variants.Cached.StringFixed(4))
		}
	}
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
	ontStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
		ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()))

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

// useHTTPMode is the serve-mode gate (R-07): an explicit --addr always
// selects HTTP mode; bare serve selects it only when the user did NOT
// pass --transport or --ui. Decided by Flags().Changed because
// --transport's default is "stdio" — comparing the string value is the
// dead-gate bug Gate 8 Q-1 caught.
func useHTTPMode(addr string, transportChanged, ui bool) bool {
	if addr != "" {
		return true
	}
	return !transportChanged && !ui
}

// runServeHTTP is the SPEC-02 unified server: workspace lock + REST +
// streamable MCP + metrics + graceful drain on one listener.
func runServeHTTP(cmd *cobra.Command, dir, addr string) error {
	workspace, _ := cmd.Flags().GetString("workspace")
	if workspace != "" {
		dir, _ = filepath.Abs(workspace)
	}
	tokenFile, _ := cmd.Flags().GetString("token-file")
	maxCompiles, _ := cmd.Flags().GetInt("max-concurrent-compiles")
	drain, _ := cmd.Flags().GetDuration("drain-timeout")
	insecure, _ := cmd.Flags().GetBool("insecure-no-auth")
	tokenFlag, _ := cmd.Flags().GetString("token")

	var configToken string
	var cfg *config.Config
	if loaded, cfgErr := config.Load(resolveConfigPath(dir)); cfgErr == nil {
		cfg = loaded
		configToken = cfg.Serve.Token
	}
	tokens, err := serve.LoadTokens(tokenFlag, tokenFile, os.Getenv("SAGE_WIKI_TOKEN"), configToken)
	if err != nil {
		return err
	}
	if err := serve.CheckRefusal(addr, tokens, insecure); err != nil {
		return err
	}

	// SPEC-07 event surfaces: bus + audit-trail file sink (+ stdout tee,
	// webhook dispatchers). Built before the engine so the open can wire
	// the sink; a webhook config error fails startup, not first delivery.
	bus, webhookStops, err := serve.BuildEventSurfaces(cmd.Context(), dir, cfg)
	if err != nil {
		// The scoped-degradation design returns a LIVE bus alongside the
		// webhook error; this surface fails startup, so tear the plane
		// down before returning (the teardown defer is not registered
		// yet on this path).
		if bus != nil {
			bus.Close()
		}
		for _, stop := range webhookStops {
			stop()
		}
		return err
	}
	// Teardown order matters (SPEC-07): bus.Close() FIRST drains the
	// remaining events into the sinks; the dispatcher stops AFTER, so its
	// shutdown drain dead-letters whatever the drain handed it — queued
	// events get a record, never a silent loss.
	defer func() {
		if bus != nil {
			bus.Close()
		}
		for _, stop := range webhookStops {
			stop()
		}
	}()

	// Bind FIRST and serve healthz/readyz immediately (AC-S1): ONE
	// listener, ONE http.Server. The handler is readiness-aware — until
	// the build completes it answers healthz/readyz (503) only; at
	// handoff the handler is swapped atomically to the full surface (N-01:
	// no interim server, no closed listener).
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	var ready atomic.Bool
	var liveHandler atomic.Value // http.Handler, installed at handoff
	lim := limits.Limits{}
	if cfg != nil {
		lim = cfg.Limits
	}
	httpSrv := serve.NewHardenedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := liveHandler.Load().(http.Handler); ok && h != nil {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/healthz":
			w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			if ready.Load() {
				w.Write([]byte(`{"status":"ready"}`))
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"status":"starting"}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"server is starting"}}`))
		}
	}), lim)
	go httpSrv.Serve(listener)

	// Workspace lock (§2.0): the read-write open acquires engine.lock and
	// fails fast when another process holds it.
	w, err := engine.Open(cmd.Context(), dir, engine.WithEventSink(bus))
	if err != nil {
		httpSrv.Shutdown(context.Background())
		return err
	}
	// SPEC-07: single-workspace serve opens without a Manager, so it
	// records the open-workspaces gauge itself (the Manager records it in
	// multi mode; lazy registration would otherwise hide the series here).
	metrics.GaugeNamed("workspaces_open").Inc()
	defer metrics.GaugeNamed("workspaces_open").Dec()
	defer w.Close() // idempotent; Shutdown may close it first
	fmt.Fprintf(os.Stderr, "sage-wiki serve (HTTP) — workspace %s locked for exclusive use\n", dir)

	deps, err := serve.AssembleDeps(dir)
	if err != nil {
		httpSrv.Shutdown(context.Background())
		return err
	}
	deps.SetEventSink(bus) // serve-path compiles bypass the engine (SPEC-07)
	defer deps.Close()
	mcpSrv, err := mcppkg.NewServer(dir, deps.Coordinator())
	if err != nil {
		httpSrv.Shutdown(context.Background())
		return err
	}
	mcpSrv.SetEventSink(bus) // on-demand compiles join the plane (SPEC-07)
	defer mcpSrv.Close()

	srv, err := serve.New(deps, mcpSrv, serve.Config{
		Workspace:             dir,
		Tokens:                tokens,
		MaxConcurrentCompiles: maxCompiles,
		DrainTimeout:          drain,
		Addr:                  addr,
		ReadyFn:               ready.Load,
		Bus:                   bus,
	})
	if err != nil {
		httpSrv.Shutdown(context.Background())
		return err
	}
	srv.SetWorkspace(w)

	// Build complete: swap to the full handler and flip readyz (N-01:
	// atomic swap on ONE server — no Shutdown, no closed listener).
	srv.InjectHTTPServer(httpSrv)
	liveHandler.Store(srv.Handler())
	ready.Store(true)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if deps.WorkerEnabled() {
		deps.StartWorker(ctx)
	}
	deps.StartMirror(ctx)
	fmt.Fprintf(os.Stderr, "sage-wiki serve (HTTP) listening on %s — REST at /, MCP at /mcp, metrics at /metrics\n", addr)
	go srv.StartQueue(ctx)
	<-ctx.Done()
	return srv.Shutdown()
}

// runServeMulti is the SPEC-06 multi-workspace server: one root listener,
// /v1/workspaces + /w/{name}/... routed to lazily assembled per-workspace
// stacks behind the engine.Manager (LRU-bounded live workspaces).
func runServeMulti(cmd *cobra.Command, root, addr string) error {
	tokenFile, _ := cmd.Flags().GetString("token-file")
	maxCompiles, _ := cmd.Flags().GetInt("max-concurrent-compiles")
	drain, _ := cmd.Flags().GetDuration("drain-timeout")
	insecure, _ := cmd.Flags().GetBool("insecure-no-auth")
	tokenFlag, _ := cmd.Flags().GetString("token")

	tokens, err := serve.LoadTokens(tokenFlag, tokenFile, os.Getenv("SAGE_WIKI_TOKEN"), "")
	if err != nil {
		return err
	}
	if err := serve.CheckRefusal(addr, tokens, insecure); err != nil {
		return err
	}

	abs, _ := filepath.Abs(root)
	maxOpen, _ := cmd.Flags().GetInt("max-open")
	idleClose, _ := cmd.Flags().GetDuration("idle-close")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ms, err := serve.NewMulti(ctx, serve.MultiConfig{
		Root:                  abs,
		Tokens:                tokens,
		MaxOpen:               maxOpen,
		IdleClose:             idleClose,
		MaxConcurrentCompiles: maxCompiles,
		DrainTimeout:          drain,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "sage-wiki serve (multi-workspace) listening on %s — root %s, workspaces at /w/{name}/, list at /v1/workspaces\n", addr, abs)
	return ms.Serve(ctx, addr)
}

// printPartsDiff prints old→new for each differing key component (spec
// §Observability's human-table line item).
func printPartsDiff(ex *engine.CompileExplanation) {
	diffs := []struct{ name, old, new string }{
		{"source", ex.StoredParts.Source, ex.CurrentParts.Source},
		{"pipeline", ex.StoredParts.Pipeline, ex.CurrentParts.Pipeline},
		{"templates", ex.StoredParts.Templates, ex.CurrentParts.Templates},
		{"models", ex.StoredParts.Models, ex.CurrentParts.Models},
		{"config", ex.StoredParts.Config, ex.CurrentParts.Config},
		{"embed", ex.StoredParts.Embed, ex.CurrentParts.Embed},
	}
	for _, d := range diffs {
		if d.old != d.new {
			old := d.old
			if old == "" {
				old = "(none)"
			}
			fmt.Printf("  %s: %s → %s\n", d.name, old, d.new)
		}
	}
}
