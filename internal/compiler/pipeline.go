package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/auth"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/fsutil"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/sourcedate"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/events"
	publicprovider "github.com/xoai/sage-wiki/pkg/provider"
)

// CompileOpts configures a compilation run.
type CompileOpts struct {
	// Backend, when set, supplies the storage stack (P2-1 T9a-2); nil uses
	// the legacy sqlite open. Caller retains Backend ownership.
	Backend store.Backend

	// Ctx carries cancellation (Ctrl-C / MCP deadline) into the LLM calls and
	// compile passes; nil is treated as context.Background().
	Ctx     context.Context
	DryRun  bool
	Fresh   bool             // ignore checkpoint
	Force   bool             // SPEC-04 R1: recompile every doc, keys recomputed
	Batch   bool             // use batch API (async, 50% discount)
	NoCache bool             // disable prompt caching
	Prune   bool             // delete orphaned articles when sources removed
	Tracker *llm.CostTracker // optional cost tracker
	// CompletionProvider, when set by pkg/engine, replaces config-backed LLM
	// completions for this run. Direct compiler callers leave it nil.
	CompletionProvider publicprovider.Provider

	// Prompts, when set, is the per-workspace template registry (SPEC-01) —
	// overrides load into it instead of the process-global prompts default,
	// so two workspaces never share render state. nil = package default.
	Prompts *prompts.Registry

	// Per-run overrides (pkg/engine CompileRequest); unset = config.
	Tier    *int // nil = use config; 0..3 overrides default_tier
	Model   string
	MaxDocs int
	// MaxCost stops the run between passes once accumulated cost exceeds
	// it (partial result + ErrBudgetExceeded). nil = no guard.
	MaxCost *decimal.Decimal

	// Recorder, when set, receives usage events instead of the default
	// workspace file ledger + event-sink bridge (SPEC-07: entry paths that
	// want events leave this nil and set Sink).
	Recorder llm.UsageRecorder

	// Sink, when set, receives the compile-lifecycle events (SPEC-07):
	// compile_started/finished, per-doc outcomes, skips, usage bridge,
	// promotions. nil = no events.
	Sink events.Sink

	// JobID identifies this compile job in its events; empty = generated.
	JobID string

	// IsInterrupted, when set, reports whether a context cancellation came
	// from a serve-queue stop (Outcome "interrupted") rather than the
	// request itself (Outcome "cancelled"). nil = always "cancelled".
	IsInterrupted func() bool

	// Progress, when set, is the event hub the pipeline reports into (P2-3 —
	// the TUI and the serve worker share one so subscribers see live events);
	// nil creates a fresh stderr-only tracker.
	Progress *Progress
}

// CompileResult summarizes what happened during compilation.
type CompileResult struct {
	Added             int
	Modified          int
	Removed           int
	Summarized        int
	ConceptsExtracted int
	ArticlesWritten   int
	Errors            int
	EmbedErrors       int
	CostReport        *llm.CostReport // nil if no LLM calls were made
	TierIndexed       int             // sources indexed at Tier 0
	TierEmbedded      int             // sources embedded at Tier 1
	TierCompiled      int             // sources sent through full pipeline (Tier 3)
	// SPEC-04: docs skipped/adopted by the compile-key evaluation.
	Skipped []SkippedDoc
	Adopted int
	// JobID is the compile job identifier used in this run's events
	// (SPEC-07): the caller's CompileOpts.JobID or a generated one.
	JobID string
}

// CompileState tracks progress for checkpoint/resume (ADR-018).
type CompileState struct {
	CompileID string         `json:"compile_id"`
	StartedAt string         `json:"started_at"`
	Pass      int            `json:"pass"`
	Completed []string       `json:"completed"`
	Pending   []string       `json:"pending"`
	Failed    []FailedSource `json:"failed,omitempty"`
	Batch     *BatchState    `json:"batch,omitempty"` // non-nil when batch is in flight
}

// BatchState tracks an in-flight batch job for checkpoint/resume.
type BatchState struct {
	BatchID     string `json:"batch_id"`
	Provider    string `json:"provider"`
	Pass        string `json:"pass"`        // which compiler pass (summarize, extract)
	ResultsRef  string `json:"results_ref"` // Anthropic: results URL; OpenAI: output_file_id
	SubmittedAt string `json:"submitted_at"`

	// PathByID maps a wire-level custom_id (a short hash of the source path)
	// back to the original source path. The wire ID is required to stay short
	// because some providers (Zhipu GLM) cap custom_id at 64 chars while OpenAI
	// and Anthropic allow long IDs. Populated by submitBatch, consumed by
	// resumeBatch. Empty for legacy checkpoints written before this fix —
	// resumeBatch then falls back to treating custom_id as the literal path.
	// Issue #89.
	PathByID map[string]string `json:"path_by_id,omitempty"`
}

// batchIDForPath produces a short stable custom_id for a source path that
// fits within every provider's custom_id length limit (Zhipu GLM caps it at
// 64 chars; OpenAI and Anthropic are more permissive). 16 hex chars (64 bits
// of SHA-256) gives birthday-paradox-safe uniqueness up to ~4B entries.
func batchIDForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

type FailedSource struct {
	Path     string `json:"path"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

// orBackground returns ctx, or context.Background() when ctx is nil, so the
// manifest lock's context-aware wait never dereferences a nil context. The
// manifest MergeSave runs on the completed-run branch where cancellation should
// no longer drop the result, but the lock still honors an explicit cancel.
func orBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// newTrackedClient builds the LLM client and attaches the cost tracker.
// Extracted so the early batch-resume path and the lazy standard path
// construct them identically (P1-3).
func newTrackedClient(projectDir string, cfg *config.Config, opts *CompileOpts) (*llm.Client, *llm.CostTracker, error) {
	providerName := cfg.API.Provider
	var client *llm.Client
	var err error
	if opts.CompletionProvider != nil {
		providerName = llm.InjectedProviderName
		client, err = llm.NewClientWithProvider(opts.CompletionProvider)
		if err == nil {
			client.SetCallTimeout(cfg.Limits.Resolve().ProviderTimeout)
		}
	} else {
		client, err = auth.NewLLMClient(cfg)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("compile: create LLM client: %w", err)
	}
	tracker := opts.Tracker
	if tracker == nil {
		var err error
		tracker, err = llm.NewCostTrackerWithTable(providerName, cfg.Compiler.TokenPriceOverride, cfg.Compiler.PriceTable)
		if err != nil {
			return nil, nil, fmt.Errorf("compile: load price registry: %w", err)
		}
	}
	client.SetTracker(tracker)
	// SPEC-05 usage ledger: one event per completion. Tier is 3 by
	// construction — the full LLM pipeline only runs for tier-3 sources
	// (runTiers claims tiers 0/1/3; LLM passes are tier-3-gated).
	// SPEC-07: the default construction is the shared bridge — file ledger
	// plus the run's event sink (nil sink → plain file ledger), so every
	// entry path (engine, serve, CLI, parity harness) emits usage events.
	recorder := opts.Recorder
	if recorder == nil {
		recorder = llm.NewBridgedRecorder(projectDir, opts.Sink)
	}
	client.SetRecorder(recorder)
	client.SetTier(3)
	return client, tracker, nil
}

// CLI queue-claim cadence (P2-3): a crashed CLI leaves leases that expire in
// 5 minutes and requeue on the next compile; the heartbeat keeps long runs
// from expiring their own claims.
const (
	cliLeaseTTL          = 5 * time.Minute
	cliHeartbeatInterval = 30 * time.Second
	claimDrainLimit      = 1 << 30 // drain mode: one invocation takes everything claimable
)

// Compile runs Pass 0 (diff) and Pass 1 (summarize) of the compiler pipeline.
// compileRun carries the shared state of a single Compile execution across
// its decomposed steps (P1-8). Field set enumerated in the P1-8 spec D3;
// statements were MOVED here verbatim from the former ~450-line Compile().
// ErrBudgetExceeded is returned (with a partial CompileResult) when a run
// stops at CompileOpts.MaxCost between passes.
var ErrBudgetExceeded = errors.New("compiler: stopped at MaxCost")

// ShuffleSourcesForTest, when non-nil, reorders the diff's Added/Modified
// slices before processing (SPEC-09 AC-P2 sabotage seam). Nil in
// production — only the parity suite's sabotage test sets it.
var ShuffleSourcesForTest func([]SourceInfo) []SourceInfo

// renderPrompt renders through the per-workspace registry when set, else
// the prompts package default (CLI back-compat).
func renderPrompt(pr *prompts.Registry, name string, data any, language string) (string, error) {
	if pr == nil {
		return prompts.Render(name, data, language)
	}
	return pr.Render(name, data, language)
}

type compileRun struct {
	cfg              *config.Config
	opts             CompileOpts
	result           *CompileResult
	mf               *manifest.Manifest
	mfPath           string
	base             *manifest.Manifest
	diff             *DiffResult
	client           *llm.Client
	tracker          *llm.CostTracker
	progress         *Progress
	db               store.DBHandle
	closeDB          func() // nil-safe; no-op when the Backend is caller-owned
	memStore         store.EntryStore
	vecStore         store.VectorStore
	chunkStore       store.ChunkStore
	embedder         embed.Embedder
	pipelineOntStore store.OntologyStore
	itemStore        store.CompileItemStore
	trustStore       store.TrustStore
	tierMgr          *TierManager
	bp               *BackpressureController
	// docBudgets enforces the per-doc compile_doc_timeout stopwatch
	// (SPEC-08 D6); created per run from the workspace limits.
	docBudgets         *DocBudgets
	exOpts             []extract.ExtractOpts
	toProcess          []SourceInfo
	pipelineIncomplete bool
	budgetExhausted    bool
	compileID          string
	// SPEC-07 event state: startedEmitted keeps CompileStarted/Finished
	// paired; skippedEmitted guards the skip-event site (populated once).
	startedEmitted bool
	skippedEmitted bool
}

// Compile runs the compiler pipeline (Pass 0 diff → tiered passes). It is a thin orchestrator over four
// extracted steps (P1-8, behavior-preserving): loadInputs → [inline Pass-0
// diff + dry-run + lazy client] → resolveMode → setupStores → runTiers →
// the unchanged tail (images, removed sources, strip, manifest save,
// changelog, trust, git, cost).
func Compile(projectDir string, opts CompileOpts) (result *CompileResult, err error) {
	if opts.JobID == "" {
		opts.JobID = events.NewID()
	}
	run := &compileRun{opts: opts, result: &CompileResult{JobID: opts.JobID}}
	compileStart := time.Now()
	// SPEC-07: CompileFinished brackets every run that emitted
	// CompileStarted (pairs, never orphans). Outcome mapping: success →
	// completed; error → failed (incl. ErrBudgetExceeded — the run did not
	// finish its work); ctx cancel → cancelled, unless the runner marks the
	// cancellation a serve-queue stop → interrupted.
	defer func() {
		// SPEC-07 metrics: one counter per job (tier/outcome) + the
		// end-to-end duration histogram. Recorded for every run that
		// reached the diff, with or without an event sink.
		if run.startedEmitted {
			tierLabel := strconv.Itoa(run.runTier())
			metrics.CounterNamed("compiles_total", "tier", tierLabel, "outcome", compileOutcome(run.opts.Ctx, run.opts.IsInterrupted, err)).Add(1)
			metrics.ObserveDuration(metrics.HistogramNamed("compile_duration_seconds", metrics.CompileBuckets(), "tier", tierLabel), compileStart)
		}
		run.emitCompileFinished(projectDir, result, err)
	}()

	// Step 1: loadInputs — config, prompts (package registry), manifest,
	// merge-base snapshot, fresh-clearing, batch checkpoint check.
	done, result, err := loadInputs(projectDir, &run.opts, run)
	if done {
		return result, err
	}
	if err != nil {
		return nil, err
	}

	// Pass 0: Diff
	log.Info("Pass 0: computing diff")
	diff, err := Diff(projectDir, run.cfg, run.mf)
	if err != nil {
		return nil, fmt.Errorf("compile: diff: %w", err)
	}
	// Test seam (SPEC-09 sabotage test): when set, reorders the diff's
	// source slices before any processing — nil in production.
	if ShuffleSourcesForTest != nil {
		diff.Added = ShuffleSourcesForTest(diff.Added)
		diff.Modified = ShuffleSourcesForTest(diff.Modified)
	}
	run.diff = diff

	// SPEC-08 D1/AC2: max_compile_batch — a run claiming more docs than the
	// cap fails fast BEFORE any doc is processed, so nothing partial
	// persists. Typed error + limit_exceeded event.
	if lim := run.cfg.Limits.Resolve(); int64(len(diff.Added)+len(diff.Modified)) > lim.MaxCompileBatch {
		le := limits.New(limits.WhichCompileBatch, lim.MaxCompileBatch, int64(len(diff.Added)+len(diff.Modified)))
		if run.opts.Sink != nil {
			run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeLimitExceeded, events.LimitExceeded{
				Which:  le.Which,
				Limit:  le.Limit,
				Got:    le.Got,
				Detail: "compile",
			}))
		}
		return nil, le
	}

	run.result.Added = len(diff.Added)
	run.result.Modified = len(diff.Modified)
	run.result.Removed = len(diff.Removed)

	// SPEC-07: the run is now committed — emit the bracket start.
	run.emitCompileStarted(projectDir, diff)

	if opts.Progress != nil {
		run.progress = opts.Progress
	} else {
		run.progress = NewProgress()
	}

	// SPEC-04: compile-key classification BEFORE the nothing-to-compile fast
	// path — adoption and drift recompiles must happen even when the content
	// diff is empty. Classification reads pre-run compile_items state, so it
	// opens its own short-lived handle (setupStores runs later).
	skipCls, err := runSkipClassification(projectDir, run, diff)
	if err != nil {
		return nil, err
	}
	run.result.Adopted = len(skipCls.adopted)
	run.result.Skipped = append(skipCls.skipped, skipCls.adopted...)
	// SPEC-07: one compile_skip + one compile_doc_finished per skipped doc
	// (migrated from pkg/engine — emitted where the verdict is made).
	run.emitSkips(projectDir)
	// Review M1: key-classified spurious-Added docs (manifest-untracked
	// tier<3) LEAVE diff.Added — otherwise they double-count (Added AND
	// Modified when drifted/resumed) and the all-skip fast path can never
	// fire for tier<3 corpora.
	if len(skipCls.classifiedSpuriousAdded) > 0 {
		remove := map[string]bool{}
		for _, p := range skipCls.classifiedSpuriousAdded {
			remove[p] = true
		}
		kept := diff.Added[:0]
		for _, s := range diff.Added {
			if !remove[s.Path] {
				kept = append(kept, s)
			}
		}
		diff.Added = kept
		run.result.Added = len(diff.Added)
	}
	// SPEC-04 §APIs: the classification lands on DiffResult (Unchanged +
	// per-entry Reason) for the engine/CLI surfaces.
	populateDiffReasons(diff, skipCls)
	if len(skipCls.drifted) > 0 || len(skipCls.resume) > 0 {
		diff.Modified = append(diff.Modified, skipCls.drifted...)
		diff.Modified = append(diff.Modified, skipCls.resume...)
		run.diff = diff
		run.result.Modified = len(diff.Modified)
	}

	// The fast path yields to pending resume work (SPEC-04 R0): resume docs
	// were appended to Modified above, so an empty set here really is empty.
	if run.result.Added == 0 && run.result.Modified == 0 && run.result.Removed == 0 {
		// Queue maintenance still runs on an empty diff (P2-3): --fresh
		// must revive dead letters even when no source changed, or a
		// dead-lettered item with an unchanged file could never recover.
		if !run.opts.DryRun {
			if b := run.opts.Backend; b != nil {
				maintainQueue(run, b.CompileItems())
			} else if sdb, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db")); err == nil {
				maintainQueue(run, NewCompileItemStore(sdb, config.NowUTC))
				sdb.Close()
			} else {
				log.Warn("queue maintenance skipped: open db failed", "error", err)
			}
		}
		if len(run.result.Skipped) > 0 || run.result.Adopted > 0 {
			fmt.Fprintf(os.Stderr, "✓ Nothing to compile — %d unchanged (skipped), %d keys adopted.\n", len(skipCls.skipped), run.result.Adopted)
		} else {
			fmt.Fprintln(os.Stderr, "✓ Nothing to compile — wiki is up to date.")
		}
		return run.result, nil
	}

	if run.opts.DryRun {
		fmt.Fprintln(os.Stderr, "Dry run — changes that would be applied:")
		for _, s := range diff.Added {
			fmt.Fprintf(os.Stderr, "  + %s (%s)\n", s.Path, s.Type)
		}
		for _, s := range diff.Modified {
			fmt.Fprintf(os.Stderr, "  ~ %s (%s)\n", s.Path, s.Type)
		}
		for _, p := range diff.Removed {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		return run.result, nil
	}

	// Create LLM client (lazy — skipped entirely when a batch resume above
	// already returned).
	run.client, run.tracker, err = newTrackedClient(projectDir, run.cfg, &run.opts)
	if err != nil {
		return nil, err
	}

	// Step 2: resolveMode — standard vs batch (subscription rule, mode
	// resolution, submitBatch handoff).
	done, result, err = resolveMode(projectDir, run)
	if done {
		return result, err
	}
	if err != nil {
		return nil, err
	}

	// Step 3: setupStores — DB, store stack, backfill, compile_items,
	// checkpoint migration. The success-path db.Close() stays at Compile
	// scope (defer hazard pinned in the plan): it must NOT move into the
	// helper, or the DB would close before runTiers and the tail execute.
	if err := setupStores(projectDir, run); err != nil {
		return nil, err
	}
	defer run.closeDB()
	defer metrics.LogSnapshot() // compile-completion snapshot (P2-2)

	// Step 4: runTiers — tiers 0/1/3 orchestration, promotions/demotions.
	runTiers(projectDir, run)
	if run.budgetExhausted {
		return run.result, ErrBudgetExceeded
	}

	// SPEC-04: persist compile keys for every doc completed this run — the
	// single storage point, gated on a COMPLETE run (P1-1: a cancelled or
	// failed run marks nothing, so an interrupted doc recompiles next time).
	if !run.pipelineIncomplete && !run.opts.DryRun {
		if err := storeCompileKeysForCompletedForMode(run.cfg, run.opts.Prompts, run.itemStore, run.opts.CompletionProvider != nil); err != nil {
			log.Warn("compile-key storage failed", "error", err)
		}
	}

	// Pass 4: Image extraction (placeholder)
	ExtractImages(projectDir, run.cfg.Output, run.toProcess)

	// Handle removed sources — detect orphans BEFORE removing from manifest
	handleRemovedSources(projectDir, run.diff.Removed, run.mf, run.memStore, run.vecStore, run.pipelineOntStore, run.opts.Prune)

	// SPEC-04: drop removed sources' compile keys AND their pass flags — a
	// re-added doc recompiles via R0 (verdict `compile: incomplete (resume)`,
	// never adopted/skipped; flags reset is what makes the spec's re-add
	// promise true and what distinguishes it from an upgrade-time empty-key
	// row, which ADOPTS).
	for _, removedPath := range run.diff.Removed {
		if err := run.itemStore.ClearCompileKey(removedPath); err != nil {
			log.Warn("clear compile key failed", "path", removedPath, "error", err)
		}
		if err := run.itemStore.InvalidatePasses(removedPath); err != nil {
			log.Warn("invalidate passes on removal failed", "path", removedPath, "error", err)
		}
	}

	// Post-compile sweep: strip [[wikilinks]] pointing at concepts that don't
	// exist on disk after this compile finished. Pass 3's writer prompts the
	// LLM to use generous cross-references, but Pass 2 extracts concepts
	// conservatively, so a wiki of a non-ML corpus can end up with the
	// majority of links being phantom. Issue #90.
	MaybeStripBrokenWikilinks(projectDir, run.cfg.Output, run.cfg.Compiler.StripBrokenLinksEnabled(), run.memStore)

	// Save manifest — unless a tiered pipeline run was interrupted before Pass 2/3
	// completed. Saving then would persist this run's half-done manifest mutations
	// (AddSource/MarkCompiled/AddConcept); skipping the Save discards them so the
	// next compile reprocesses the sources from a clean state (pre-P1-1 hard-kill
	// behavior). Any handleRemovedSources manifest edits are discarded too, but they
	// re-run next compile since the removed sources are still absent from the corpus.
	// P1-1 / C1.
	if run.pipelineIncomplete {
		log.Info("compile interrupted before Pass 2/3 completed — manifest not saved; sources will reprocess on next compile")
	} else if err := manifest.MergeSave(orBackground(run.opts.Ctx), run.mfPath, run.base, run.mf); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	}

	// Write CHANGELOG entry
	if err := writeChangelog(projectDir, run.cfg.Output, run.result, run.cfg.Compiler.UserTimeLocation()); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// FTS/vector consistency check
	if run.result.EmbedErrors > 0 {
		ftsCount, _ := run.memStore.Count()
		vecCount, _ := run.vecStore.Count()
		if ftsCount != vecCount {
			log.Warn("FTS/vector mismatch after compile", "fts", ftsCount, "vec", vecCount, "embed_errors", run.result.EmbedErrors)
		}
	}

	// Check for source changes that invalidate confirmed outputs
	if run.cfg.Trust.IncludeOutputsMode() == "verified" {
		trustStore := trust.NewStore(run.db)
		stores := trust.IndexStores{
			MemStore: run.memStore, VecStore: run.vecStore, OntStore: run.pipelineOntStore,
			ChunkStore: run.chunkStore, DB: run.db,
		}
		demoted, err := trust.CheckSourceChanges(trustStore, projectDir, &stores)
		if err != nil {
			log.Warn("trust source check failed", "error", err)
		} else if demoted > 0 {
			log.Info("demoted stale outputs", "count", demoted)
		}
	}

	// Git auto-commit
	if run.cfg.Compiler.AutoCommit {
		commitMsg := fmt.Sprintf("compile: +%d sources, %d concepts, %d articles",
			run.result.Added, run.result.ConceptsExtracted, run.result.ArticlesWritten)
		gitpkg.AutoCommit(projectDir, commitMsg)
	}

	run.progress.Summary(run.result)

	// Print cost report
	costReport := run.tracker.Report()
	if costReport.TotalTokens > 0 {
		fmt.Fprint(os.Stderr, llm.FormatReport(costReport))
		run.result.CostReport = costReport
	}

	return run.result, nil
}

// ── SPEC-07 compile-lifecycle events ───────────────────────────────────
//
// Emission is nil-safe (no sink = no-op) and workspace-scoped: the envelope
// carries the workspace NAME, payloads carry workspace-relative DocIDs — no
// paths in the stream (privacy defaults).

// emitCompileStarted brackets the run once the diff is known.
func (run *compileRun) emitCompileStarted(projectDir string, diff *DiffResult) {
	if run.startedEmitted {
		return
	}
	run.startedEmitted = true
	if run.opts.Sink == nil {
		return
	}
	run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeCompileStarted, events.CompileStarted{
		JobID:    run.opts.JobID,
		Tier:     run.runTier(),
		DocCount: len(diff.Added) + len(diff.Modified),
	}))
}

// runTier resolves the effective tier of this run (override, else config
// default) — shared by the compile_started event and the compile metrics.
func (run *compileRun) runTier() int {
	if run.opts.Tier != nil {
		return *run.opts.Tier
	}
	if run.cfg != nil {
		return run.cfg.Compiler.DefaultTier
	}
	return 3
}

// emitSkips fans one compile_skip + one compile_doc_finished per skipped
// doc (SPEC-04 pledge observable; SPEC-07 per-doc outcome).
func (run *compileRun) emitSkips(projectDir string) {
	if run.opts.Sink == nil || run.skippedEmitted || run.result == nil {
		return
	}
	run.skippedEmitted = true
	for _, s := range run.result.Skipped {
		run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeCompileSkip, events.CompileSkip{
			DocID:  s.Path,
			Reason: s.Reason,
		}))
		run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeCompileDocFinished, events.CompileDocFinished{
			JobID:   run.opts.JobID,
			DocID:   s.Path,
			Skipped: true,
		}))
	}
}

// emitDocFinished fans one compile_doc_finished for a processed doc.
func (run *compileRun) emitDocFinished(projectDir string, docID string, tier int) {
	if run.opts.Sink == nil {
		return
	}
	run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeCompileDocFinished, events.CompileDocFinished{
		JobID: run.opts.JobID,
		DocID: docID,
		Tier:  tier,
	}))
}

// emitPromotion fans one promotion_triggered for an applied tier change
// (either direction — FromTier→ToTier disambiguates).
func (run *compileRun) emitPromotion(projectDir, docID string, fromTier, toTier int, trigger string) {
	if run.opts.Sink == nil {
		return
	}
	run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypePromotionTriggered, events.PromotionTriggered{
		DocID:    docID,
		FromTier: fromTier,
		ToTier:   toTier,
		Trigger:  trigger,
	}))
}

// currentTier reads a doc's tier before a change (best effort — a read
// failure reports 0 rather than stalling the promotion itself).
func currentTier(items store.CompileItemStore, path string) int {
	if items == nil {
		return 0
	}
	item, err := items.GetByPath(path)
	if err != nil || item == nil {
		return 0
	}
	return item.Tier
}

// bindOntSink installs the run's event sink on an ontology store (SPEC-07
// edge events). The setter is narrow and type-asserted — it is not part of
// store.OntologyStore, so non-emitting implementations simply skip. The
// sink is workspace-bound here: stores do not know their workspace.
func bindOntSink(ontStore store.OntologyStore, sink events.Sink, projectDir string) {
	if sink == nil || ontStore == nil {
		return
	}
	setter, ok := ontStore.(interface{ SetEventSink(events.Sink) })
	if !ok {
		return
	}
	setter.SetEventSink(events.BindWorkspace(sink, filepath.Base(projectDir)))
}

// compileOutcome maps a finished run to the SPEC-07 outcome enum:
// completed | failed | cancelled | interrupted (serve-queue stop).
// ErrBudgetExceeded counts as failed — the run did not finish its work.
func compileOutcome(ctx context.Context, isInterrupted func() bool, err error) string {
	if err == nil {
		return "completed"
	}
	// A nil ctx can never have been cancelled — callers that construct
	// CompileOpts without Ctx (hub, TUI) must map errors to failed, not
	// cancelled.
	if ctx != nil {
		ctxErr := ctx.Err()
		if errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
			if isInterrupted != nil && isInterrupted() {
				return "interrupted"
			}
			return "cancelled"
		}
	}
	return "failed"
}

// emitCompileFinished closes the bracket opened by emitCompileStarted.
// A run that never started (early loadInputs returns) emits nothing —
// pairs, never orphans.
func (run *compileRun) emitCompileFinished(projectDir string, result *CompileResult, err error) {
	if run.opts.Sink == nil || !run.startedEmitted {
		return
	}
	outcome := compileOutcome(run.opts.Ctx, run.opts.IsInterrupted, err)
	res := result
	if res == nil {
		res = run.result
	}
	totals := events.CompileTotals{}
	if res != nil {
		totals.Docs = res.Added + res.Modified
		totals.Compiled = res.Summarized
		totals.Skipped = len(res.Skipped)
		if res.CostReport != nil {
			totals.InputTokens = res.CostReport.TotalInputTokens
			totals.CachedTokens = res.CostReport.TotalCachedTokens
			totals.OutputTokens = res.CostReport.TotalOutputTokens
			totals.Cost = res.CostReport.Cost // nil when unknown — never fabricated
		}
	}
	run.opts.Sink.Emit(events.NewEvent(filepath.Base(projectDir), events.TypeCompileFinished, events.CompileFinished{
		JobID:   run.opts.JobID,
		Outcome: outcome,
		Totals:  totals,
	}))
}

// loadInputs performs Compile's input phase: config load, prompt overrides
// (into the package-level registry — no field, by design), manifest load +
// merge-base snapshot, --fresh checkpoint clearing, and the pending-batch
// check. It returns done=true when Compile must return early (dry-run batch
// report or a batch resume), with the result/error to return verbatim.
func loadInputs(projectDir string, opts *CompileOpts, run *compileRun) (done bool, result *CompileResult, err error) {
	// Load config
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return false, nil, fmt.Errorf("compile: load config: %w", err)
	}
	run.cfg = cfg

	// Per-run overrides (pkg/engine CompileRequest) — applied to the loaded
	// config copy, never written back.
	if opts.Tier != nil {
		cfg.Compiler.DefaultTier = *opts.Tier
	}
	if opts.Model != "" {
		cfg.Models.Summarize = opts.Model
		cfg.Models.Extract = opts.Model
		cfg.Models.Write = opts.Model
	}
	if opts.CompletionProvider != nil && (opts.Batch || cfg.Compiler.Mode == "batch") {
		return false, nil, fmt.Errorf("compile: provider %s does not support batch API", llm.InjectedProviderName)
	}

	// Load user prompt overrides if prompts/ directory exists — into the
	// per-workspace registry when one was supplied (SPEC-01), else the
	// process-global default (CLI behavior unchanged).
	promptsDir := filepath.Join(projectDir, "prompts")
	if opts.Prompts != nil {
		if err := opts.Prompts.LoadFromDir(promptsDir); err != nil {
			log.Warn("failed to load custom prompts", "error", err)
		}
	} else if err := prompts.LoadFromDir(promptsDir); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}

	// Load manifest
	run.mfPath = filepath.Join(projectDir, ".manifest.json")
	mf, err := manifest.Load(run.mfPath)
	if err != nil {
		return false, nil, fmt.Errorf("compile: load manifest: %w", err)
	}
	mf.SetNow(config.NowUTC)
	run.mf = mf

	// Snapshot the manifest at Load as the merge base (D3). At Save time the
	// compile reloads fresh under the lock and applies its delta (ours-base) so a
	// short writer (MCP/CLI/ingest) that landed mid-compile is preserved rather
	// than clobbered. Taken before any mutation, so ours-base is exactly this
	// run's mutations.
	run.base = mf.Clone()

	// P1-3: --fresh clears ALL checkpoint state (batch + legacy), not just
	// skips it — the provider-mismatch error below tells users to "clear
	// checkpoint with --fresh", and a skip-without-clear would strand them in
	// an unrecoverable mismatch loop. NOT under --dry-run: pre-P1-3,
	// --fresh --dry-run was fully side-effect-free, and deleting a paid
	// in-flight batch ID on a preview command is not an acceptable change.
	if opts.Fresh && !opts.DryRun {
		clearAllCheckpoints(projectDir)
	}

	// Check for a pending batch BEFORE the diff and its early returns: the
	// batch's sources may all be gone from disk (empty diff) and the batch
	// must still be consumed. Batch state lives only in
	// .sage/batch-state.json; a legacy compile-state.json in-flight batch is
	// split into it here (spec D2/D3). The LLM client is created early ONLY
	// on this path; the standard path creates it lazily below.
	if !opts.Fresh {
		bcp, err := loadOrMigrateBatchCheckpoint(projectDir)
		if err != nil {
			return false, nil, fmt.Errorf("compile: load batch checkpoint: %w", err)
		}
		if bcp != nil && bcp.Batch == nil {
			// Dead checkpoint (parseable but batch-less) — no writer produces
			// this today, but don't reload it forever: remove and continue.
			// Skipped under --dry-run, which defers ALL checkpoint deletion.
			if !opts.DryRun {
				log.Warn("removing dead batch checkpoint (no batch state)", "path", batchCheckpointPath(projectDir))
				if err := os.Remove(batchCheckpointPath(projectDir)); err != nil {
					log.Warn("failed to remove dead batch checkpoint", "error", err)
				}
			}
			bcp = nil
		}
		if bcp != nil && bcp.Batch != nil {
			if opts.DryRun {
				// Dry-run contract: report, don't act. The one-time split
				// above MAY have run (idempotent metadata migration) — only
				// polling, summaries, and checkpoint deletion are deferred.
				fmt.Fprintf(os.Stderr, "Batch %s pending resume (dry run — provider not polled, no summaries written).\n", bcp.Batch.BatchID)
				return true, run.result, nil
			}
			client, tracker, err := newTrackedClient(projectDir, run.cfg, opts)
			if err != nil {
				return false, nil, err
			}
			run.client, run.tracker = client, tracker
			if client.ProviderName() != bcp.Batch.Provider {
				return false, nil, fmt.Errorf("compile: provider changed from %s to %s since batch was submitted — clear checkpoint with --fresh or switch back", bcp.Batch.Provider, client.ProviderName())
			}
			res, rerr := resumeBatch(projectDir, client, run.cfg, run.mf, run.base, bcp, tracker, *opts)
			return true, res, rerr
		}
	}
	return false, nil, nil
}

// resolveMode picks standard vs batch compilation (subscription-auth rule,
// CLI > config > auto threshold) and hands off to submitBatch on the batch
// path. done=true means Compile returns its result verbatim.
func resolveMode(projectDir string, run *compileRun) (done bool, result *CompileResult, err error) {
	cfg := run.cfg
	opts := &run.opts

	// Subscription auth: disable batch mode (subscription tokens lack batch API access)
	if cfg.API.Auth == "subscription" && (opts.Batch || cfg.Compiler.Mode == "batch" || cfg.Compiler.Mode == "auto") {
		log.Info("batch mode unavailable with subscription auth, using standard mode")
		fmt.Fprintln(os.Stderr, "Batch mode unavailable with subscription auth, using standard mode.")
		opts.Batch = false
		if cfg.Compiler.Mode == "batch" || cfg.Compiler.Mode == "auto" {
			cfg.Compiler.Mode = "standard"
		}
	}

	// Resolve batch mode: CLI flag > config mode > default (standard)
	useBatch := opts.Batch
	if !useBatch && cfg.Compiler.Mode == "batch" {
		useBatch = true
	}
	if !useBatch && cfg.Compiler.Mode == "auto" && run.client.SupportsBatch() {
		sourceCount := len(run.diff.Added) + len(run.diff.Modified)
		threshold := cfg.Compiler.BatchThreshold
		if threshold <= 0 {
			threshold = 10 // default: auto-batch when 10+ sources
		}
		if sourceCount >= threshold {
			useBatch = true
			log.Info("auto-selecting batch mode", "sources", sourceCount, "threshold", threshold)
		}
	}
	if useBatch {
		if !run.client.SupportsBatch() {
			return false, nil, fmt.Errorf("compile: provider %s does not support batch API", cfg.API.Provider)
		}
		res, rerr := submitBatch(projectDir, run.client, cfg, run.mf, run.diff, run.tracker, run.opts.Prompts)
		return true, res, rerr
	}
	return false, nil, nil
}

// setupStores opens the project DB and builds the per-compile store stack,
// then runs the post-migration backfills: chunk index backfill,
// compile_items population from the manifest, and legacy checkpoint
// migration. On success the DB is OPEN and ownership is Compile's (the
// deferred close stays at Compile scope — plan T5's pinned hazard).
func setupStores(projectDir string, run *compileRun) error {
	cfg := run.cfg

	// P2-1 T9a-2: when the caller injects a Backend, the store stack comes
	// from its accessors (postgres-ready) and the caller retains ownership;
	// otherwise the legacy sqlite open builds the concrete stack (compiler
	// cannot import storedial — import cycle).
	var db store.DBHandle
	if b := run.opts.Backend; b != nil {
		db = b
		run.closeDB = func() {}
		run.memStore = b.Entries()
		run.vecStore = b.Vectors()
		run.chunkStore = b.Chunks()
		run.pipelineOntStore = b.Ontology()
		run.itemStore = b.CompileItems()
		// The Backend's own Trust store: trust.NewStore is the SQLITE
		// implementation and emits '?' placeholders — under
		// storage.backend=postgres every call on it fails (Gate-3 i2).
		run.trustStore = b.Trust()
	} else {
		dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
		sdb, err := storage.Open(dbPath)
		if err != nil {
			return fmt.Errorf("compile: open db: %w", err)
		}
		db = sdb
		run.closeDB = func() { sdb.Close() }
		run.memStore = memory.NewStore(sdb)
		run.vecStore = vectors.NewStore(sdb)
		run.chunkStore = memory.NewChunkStore(sdb)
		run.trustStore = trust.NewStore(sdb)
		merged := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		run.pipelineOntStore = ontology.NewStore(sdb, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
	}
	run.db = db

	run.embedder = embed.NewFromConfig(cfg)

	merged := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	if run.pipelineOntStore == nil {
		run.pipelineOntStore = ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
	}
	// SPEC-07: edge events flow through the run's sink whichever backend
	// supplied the store (sqlite Store or postgres twin).
	bindOntSink(run.pipelineOntStore, run.opts.Sink, projectDir)

	// Backfill chunk index if needed (after migration, before first compile)
	if run.chunkStore.NeedsBackfill(run.memStore) {
		log.Info("chunk index empty with existing articles — running backfill")
		if _, err := BackfillChunks(projectDir, cfg.Output, cfg.Search.ChunkSizeOrDefault(), cfg.Search.ChunkOverlapOrDefault(), run.chunkStore, run.vecStore, run.embedder, db); err != nil {
			log.Warn("chunk backfill failed", "error", err)
		}
	}

	// Initialize compile_items store and tier manager
	if run.itemStore == nil {
		run.itemStore = NewCompileItemStore(db, config.NowUTC)
	}
	run.tierMgr = NewTierManager(&cfg.Compiler, run.itemStore)
	run.bp = NewBackpressureController(providerConcurrency(cfg))
	// SPEC-08 D6: one stopwatch registry per run; each doc's budget equals
	// limits.compile_doc_timeout. The registry is cheap (lazy per-doc map).
	run.docBudgets = NewDocBudgets(cfg.Limits.Resolve().CompileDocTimeout)

	// Populate compile_items from manifest on first run (if empty)
	if count, _ := run.itemStore.Count(); count == 0 && run.mf.SourceCount() > 0 {
		populated, err := PopulateFromManifest(run.itemStore, run.mf, cfg)
		if err != nil {
			log.Warn("populate compile_items from manifest failed", "error", err)
		} else if populated > 0 {
			log.Info("populated compile_items from manifest", "count", populated)
		}
	}

	// Migrate legacy checkpoint if present
	if !run.opts.Fresh {
		if migrated, err := MigrateCheckpoint(projectDir, run.itemStore, run.mf, cfg); err != nil {
			log.Warn("checkpoint migration failed", "error", err)
		} else if migrated {
			log.Info("legacy checkpoint migrated to compile_items")
		}
	}

	return nil
}

// maintainQueue performs the CLI's queue housekeeping (P2-3): reset dead
// letters under --fresh (never under --dry-run) and requeue expired
// leases. Runs on every compile — including the empty-diff early return,
// so recovery paths don't depend on source changes.
func maintainQueue(run *compileRun, items store.CompileItemStore) {
	if run.opts.Fresh && !run.opts.DryRun {
		if n, err := items.ResetFailed(); err != nil {
			log.Error("queue reset of dead-lettered items failed", "error", err)
			run.result.Errors++
		} else if n > 0 {
			log.Info("--fresh: reset dead-lettered queue items", "count", n)
		}
	}
	if n, err := items.RequeueExpired(time.Now().UTC()); err != nil {
		log.Error("queue requeue of expired leases failed", "error", err)
		run.result.Errors++
	} else if n > 0 {
		log.Info("requeued expired compile leases", "count", n)
	}
}

// providerConcurrency resolves the effective provider-call ceiling
// (SPEC-08 D1/D6): the backpressure controller runs at
// min(compiler.max_parallel, limits.max_concurrent_provider_calls).
func providerConcurrency(cfg *config.Config) int {
	maxParallel := cfg.Compiler.MaxParallel
	ceiling := int(cfg.Limits.Resolve().MaxConcurrentProviderCalls)
	if ceiling > 0 && (maxParallel <= 0 || ceiling < maxParallel) {
		return ceiling
	}
	if maxParallel <= 0 {
		return 20 // BackpressureController's documented default
	}
	return maxParallel
}

// runTiers executes the tiered compilation passes (Tier 0 FTS index, Tier 1
// index+embed, Tier 3 full LLM pipeline) plus promotion/demotion checks,
// mutating run.result and run.pipelineIncomplete exactly as the inline code
// did before P1-8.
func runTiers(projectDir string, run *compileRun) {
	defer metrics.LogSnapshot() // phase-end snapshot (P2-2)
	cfg := run.cfg
	opts := &run.opts

	// Queue lifecycle (P2-3): the CLI is an in-process worker of one. It
	// requeues crashed leases, resets dead letters under --fresh, then
	// claims/processes/releases per tier with heartbeats, so a killed
	// compile leaves resumable queue state exactly like the server worker.
	cliToken := fmt.Sprintf("cli-%d-%d", os.Getpid(), time.Now().UnixNano())
	wc := ResolveWorkerConfig(cfg.Serve.Worker)
	maintainQueue(run, run.itemStore)

	// Resolve tiers and upsert compile_items for new/modified sources
	allSources := append(run.diff.Added, run.diff.Modified...)
	upsertDiffItems(run, projectDir, allSources)

	// Load external parsers if enabled and trusted
	if cfg.Parsers.External {
		if !cfg.Parsers.TrustExternal {
			fmt.Fprintln(os.Stderr, "Warning: parsers.external is true but parsers.trust_external is false.")
			fmt.Fprintln(os.Stderr, "External parsers execute arbitrary code WITHOUT a sandbox on most systems.")
			fmt.Fprintln(os.Stderr, "Set parsers.trust_external: true in config.yaml to acknowledge this risk.")
		} else {
			exReg, err := extract.LoadExternalParsers(projectDir)
			if err != nil {
				log.Warn("loading external parsers", "error", err)
			} else if exReg.HasParsers() {
				exReg.Trusted = true
				fmt.Fprintln(os.Stderr, "External parsers enabled (trusted mode). Running parser code from this project.")
				run.exOpts = []extract.ExtractOpts{{ExternalParsers: exReg, ParsersEnabled: true}}
			}
		}
	}

	// Tier 0: FTS5 index only (no LLM, ~5ms/doc)
	tier0Claimed, claimErr0 := run.itemStore.Claim(0, cliToken, cliLeaseTTL, claimDrainLimit)
	if claimErr0 != nil {
		log.Error("queue claim failed — tier 0 sources not processed this run", "error", claimErr0)
		run.result.Errors++
	}
	var indexed int
	if len(tier0Claimed) > 0 {
		run.progress.StartPhase("Tier 0: Index sources", len(tier0Claimed))
		stopHB := startItemHeartbeat(run.itemStore, cliToken, tier0Claimed, cliHeartbeatInterval, cliLeaseTTL)
		func() {
			defer stopHB()
			indexed = indexRawSources(projectDir, tier0Claimed, run.memStore, run.itemStore, run.exOpts...)
		}()
		errored0 := erroredSinceClaim(run.itemStore, tier0Claimed)
		releaseClaimed(run.itemStore, cliToken, tier0Claimed, errored0, wc.MaxAttempts)
		for _, item := range tier0Claimed {
			if !errored0[item.SourcePath] {
				run.emitDocFinished(projectDir, item.SourcePath, 0)
			}
		}
		run.result.TierIndexed = indexed
		log.Info("tier 0 indexing complete", "indexed", indexed)
		run.progress.EndPhase()
	}

	// Tier 1: FTS5 + vector embed (~200ms/doc)
	tier1Claimed, claimErr1 := run.itemStore.Claim(1, cliToken, cliLeaseTTL, claimDrainLimit)
	if claimErr1 != nil {
		log.Error("queue claim failed — tier 1 sources not processed this run", "error", claimErr1)
		run.result.Errors++
	}
	var embedded int
	if len(tier1Claimed) > 0 {
		run.progress.StartPhase("Tier 1: Index + embed sources", len(tier1Claimed))
		stopHB := startItemHeartbeat(run.itemStore, cliToken, tier1Claimed, cliHeartbeatInterval, cliLeaseTTL)
		func() {
			defer stopHB()
			indexed, embedded = indexAndEmbedSources(run.opts.Ctx, projectDir, tier1Claimed, run.memStore, run.vecStore, run.embedder, run.itemStore, run.bp, run.chunkStore, cfg.Search.ChunkSizeOrDefault(), cfg.Search.ChunkOverlapOrDefault(), run.db, run.exOpts...)
		}()
		errored1 := erroredSinceClaim(run.itemStore, tier1Claimed)
		releaseClaimed(run.itemStore, cliToken, tier1Claimed, errored1, wc.MaxAttempts)
		for _, item := range tier1Claimed {
			if !errored1[item.SourcePath] {
				run.emitDocFinished(projectDir, item.SourcePath, 1)
			}
		}
		run.result.TierIndexed += indexed
		run.result.TierEmbedded = embedded
		log.Info("tier 1 indexing complete", "indexed", indexed, "embedded", embedded)
		run.progress.EndPhase()
	}

	// Tier 3: Full LLM pipeline (Pass 1 → 2 → 3) — only for Tier 3 sources
	tier3Claimed, claimErr3 := run.itemStore.Claim(3, cliToken, cliLeaseTTL, claimDrainLimit)
	if claimErr3 != nil {
		log.Error("queue claim failed — tier 3 sources not processed this run", "error", claimErr3)
		run.result.Errors++
	}
	tier3Set := make(map[string]bool)
	for _, item := range tier3Claimed {
		tier3Set[item.SourcePath] = true
	}
	for _, s := range allSources {
		if tier3Set[s.Path] {
			run.toProcess = append(run.toProcess, s)
		}
	}
	// MaxDocs guard (pkg/engine CompileRequest): deterministic truncation —
	// toProcess is in diff order, so the first N are taken.
	if opts.MaxDocs > 0 && len(run.toProcess) > opts.MaxDocs {
		log.Info("MaxDocs guard: truncating compile set", "total", len(run.toProcess), "max", opts.MaxDocs)
		run.toProcess = run.toProcess[:opts.MaxDocs]
	}

	// pipelineIncomplete is set when a tiered pipeline run does not complete Pass
	// 2/3 (cancelled or a total-extraction failure). Such a run persists NO new
	// compile state: the compile_items MarkPass advances are skipped below and the
	// manifest Save at the end of Compile is skipped, discarding this run's
	// in-memory manifest mutations (AddSource / MarkCompiled / AddConcept). The next
	// compile's Diff then re-includes these sources and reprocesses them cleanly.
	errored3 := make(map[string]bool, len(tier3Claimed))
	if len(run.toProcess) > 0 {
		cacheEnabled := cfg.Compiler.PromptCacheEnabled() && !opts.NoCache
		if cacheEnabled && cfg.API.Auth == "subscription" && cfg.API.Provider == "gemini" {
			cacheEnabled = false
			log.Info("prompt caching unavailable with Gemini subscription auth")
			fmt.Fprintln(os.Stderr, "Prompt caching unavailable with Gemini subscription auth.")
		}
		var pipelineResult *FullPipelineResult
		stopHB := startItemHeartbeat(run.itemStore, cliToken, tier3Claimed, cliHeartbeatInterval, cliLeaseTTL)
		func() {
			defer stopHB()
			pipelineResult = runFullPipeline(run.toProcess, FullPipelineOpts{
				Ctx:          opts.Ctx,
				ProjectDir:   projectDir,
				Config:       cfg,
				Client:       run.client,
				Manifest:     run.mf,
				DB:           run.db,
				MemStore:     run.memStore,
				VecStore:     run.vecStore,
				ChunkStore:   run.chunkStore,
				OntStore:     run.pipelineOntStore,
				TrustStore:   run.trustStore,
				Embedder:     run.embedder,
				Backpressure: run.bp,
				MaxCost:      opts.MaxCost,
				Tracker:      run.tracker,
				ItemStore:    run.itemStore,
				CacheEnabled: cacheEnabled,
				Progress:     run.progress,
				Sink:         opts.Sink,
				Budgets:      run.docBudgets,
				JobID:        opts.JobID,
				Prompts:      opts.Prompts,
			})
		}()
		run.result.Summarized = pipelineResult.Summarized
		run.result.ConceptsExtracted = pipelineResult.ConceptsExtracted
		run.result.ArticlesWritten = pipelineResult.ArticlesWritten
		run.result.Errors += pipelineResult.Errors
		run.result.EmbedErrors = pipelineResult.EmbedErrors
		run.result.TierCompiled = len(run.toProcess)

		// When Pass 2/3 did not complete (cancel or total-extraction failure), this
		// run is incomplete: mark nothing and (below) skip the manifest Save, so no
		// new compile state is persisted. Advancing even the "summarized" flag or
		// saving the manifest would record half-done work — the earlier surgical
		// rollback (RemoveSource on just the sources) left the run's concepts
		// orphaned, since RemoveSource deletes Sources only. P1-1 / C1.
		run.pipelineIncomplete = !pipelineResult.Pass23Completed
		if pipelineResult.BudgetExhausted {
			run.budgetExhausted = true
		}
		succeeded := make(map[string]bool)
		for _, p := range pipelineResult.SucceededSources {
			succeeded[p] = true
		}
		if !run.pipelineIncomplete {
			// Pass 2/3 completed — advance all three pass flags for the sources that
			// summarized successfully so ListPending treats them as fully compiled.
			for _, s := range run.toProcess {
				if succeeded[s.Path] {
					for _, pass := range []string{"summarized", "extracted", "written"} {
						if err := run.itemStore.MarkPass(s.Path, pass); err != nil {
							log.Warn("mark pass failed", "path", s.Path, "pass", pass, "error", err)
						}
					}
					run.emitDocFinished(projectDir, s.Path, 3)
				}
			}
		}
		// Only PROCESSED items can fail. Items claimed but absent from the
		// diff (e.g. auto-promoted sources, hash unchanged) are released
		// without budget burn below — charging them would dead-letter
		// healthy sources the CLI never compiles (independent-review CRITICAL).
		for _, s := range run.toProcess {
			if run.pipelineIncomplete || !succeeded[s.Path] {
				errored3[s.Path] = true
			}
		}
	}
	// Settle every tier-3 lease, processed or not — even when toProcess was
	// empty, or the claims would sit leased until expiry every run.
	releaseClaimed(run.itemStore, cliToken, tier3Claimed, errored3, wc.MaxAttempts)

	// Check promotions/demotions
	if cfg.Compiler.AutoPromoteEnabled() {
		if promoted, err := run.tierMgr.CheckPromotions(); err == nil && len(promoted) > 0 {
			log.Info("sources eligible for promotion", "count", len(promoted))
			for _, p := range promoted {
				fromTier := currentTier(run.itemStore, p)
				if err := run.itemStore.SetTier(p, 3, "auto-promote"); err != nil {
					log.Warn("set tier failed", "path", p, "tier", 3, "error", err)
					continue
				}
				run.emitPromotion(projectDir, p, fromTier, 3, "auto-promote")
			}
		}
	}
	if cfg.Compiler.AutoDemoteEnabled() {
		if demoted, err := run.tierMgr.CheckDemotions(); err == nil && len(demoted) > 0 {
			log.Info("demoting stale sources", "count", len(demoted))
			for _, p := range demoted {
				fromTier := currentTier(run.itemStore, p)
				if err := run.itemStore.SetTier(p, 1, "stale"); err != nil {
					log.Warn("set tier failed", "path", p, "tier", 1, "error", err)
					continue
				}
				run.emitPromotion(projectDir, p, fromTier, 1, "stale")
			}
		}
	}
}

// upsertDiffItems registers added/modified diff sources in compile_items
// with their resolved tiers. Shared by runTiers (CLI) and the worker's
// enqueue scan (P2-3) so the two paths can never diverge.
func upsertDiffItems(run *compileRun, projectDir string, allSources []SourceInfo) {
	run.compileID = config.NowUTC().Format("20060102-150405")
	for _, src := range allSources {
		tier := run.tierMgr.ResolveTier(src.Path, projectDir, nil)
		run.itemStore.Upsert(CompileItem{
			SourcePath:  src.Path,
			Hash:        src.Hash,
			FileType:    src.Type,
			SizeBytes:   src.Size,
			Tier:        tier,
			TierDefault: run.tierMgr.ConfigDefault(src.Path),
			SourceType:  "compiler",
			CompileID:   run.compileID,
		})
	}
}

// submitBatch builds batch requests from the diff and submits them.
// Saves the batch checkpoint (.sage/batch-state.json) and exits — the next
// `compile` resumes via resumeBatch.
func submitBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	diff *DiffResult,
	tracker *llm.CostTracker,
	pr *prompts.Registry,
) (*CompileResult, error) {
	result := &CompileResult{
		Added:    len(diff.Added),
		Modified: len(diff.Modified),
		Removed:  len(diff.Removed),
	}

	toProcess := append(diff.Added, diff.Modified...)
	if len(toProcess) == 0 {
		fmt.Fprintln(os.Stderr, "✓ Nothing to batch — wiki is up to date.")
		return result, nil
	}

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxTokens := cfg.Compiler.SummaryMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	// Load external parsers for batch extraction
	var batchExOpts []extract.ExtractOpts
	if cfg.Parsers.External && cfg.Parsers.TrustExternal {
		exReg, err := extract.LoadExternalParsers(projectDir)
		if err == nil && exReg.HasParsers() {
			exReg.Trusted = true
			batchExOpts = []extract.ExtractOpts{{ExternalParsers: exReg, ParsersEnabled: true}}
		}
	}

	// Build batch requests — extract content and render prompts.
	// pathByID maps each wire-level custom_id (a hash of the path) back to
	// the source path; persisted in BatchState for resumeBatch (issue #89).
	var requests []llm.BatchRequest
	pathByID := make(map[string]string)
	for _, src := range toProcess {
		absPath := filepath.Join(projectDir, src.Path)
		content, err := extract.Extract(absPath, src.Type, batchExOpts...)
		if err != nil {
			log.Warn("batch: skip source (extract failed)", "path", src.Path, "error", err)
			continue
		}

		// Skip image sources — batch doesn't support vision
		if extract.IsImageSource(content) {
			log.Info("batch: skip image source (requires vision)", "path", src.Path)
			continue
		}

		extract.ChunkIfNeeded(content, maxTokens*2)

		// Only batch single-chunk sources — multi-chunk requires sequential synthesis
		if content.ChunkCount > 1 {
			log.Info("batch: skip multi-chunk source (requires synthesis)", "path", src.Path, "chunks", content.ChunkCount)
			continue
		}

		templateName := "summarize_" + content.Type
		if _, err := renderPrompt(pr, templateName, prompts.SummarizeData{}, ""); err != nil {
			templateName = "summarize_article"
		}

		prompt, err := renderPrompt(pr, templateName, prompts.SummarizeData{
			SourcePath: src.Path,
			SourceType: content.Type,
			MaxTokens:  maxTokens,
		}, cfg.Language)
		if err != nil {
			log.Warn("batch: skip source (prompt render failed)", "path", src.Path, "error", err)
			continue
		}

		customID := batchIDForPath(src.Path)
		pathByID[customID] = src.Path
		requests = append(requests, llm.BatchRequest{
			CustomID: customID,
			Messages: []llm.Message{
				{Role: "system", Content: "You are a research assistant creating structured summaries for a personal knowledge wiki."},
				{Role: "user", Content: prompt + "\n\n" + prompts.WrapUntrusted(content.Text)},
			},
			Opts: llm.CallOpts{Model: model, MaxTokens: maxTokens, Temperature: cfg.Compiler.CompileTemperature()},
		})
	}

	if len(requests) == 0 {
		fmt.Fprintln(os.Stderr, "✓ No sources eligible for batch processing.")
		return result, nil
	}

	log.Info("submitting batch", "sources", len(requests), "provider", client.ProviderName())
	batchID, err := client.SubmitBatch(requests)
	if err != nil {
		return nil, fmt.Errorf("compile: submit batch: %w", err)
	}

	// Pending list holds source PATHS (used by resumeBatch's result
	// validation) — the wire-level IDs are kept in BatchState.PathByID.
	// Sorted for deterministic, diff-friendly checkpoint files.
	pending := make([]string, 0, len(pathByID))
	for _, path := range pathByID {
		pending = append(pending, path)
	}
	sort.Strings(pending)

	// Save the batch checkpoint — the ONLY checkpoint written on this path
	// (P1-3; no compile-state.json anywhere).
	utcNow := config.NowUTC().Format(time.RFC3339)
	bcp := &BatchCheckpoint{
		CompileID: config.NowUTC().Format("20060102-150405"),
		StartedAt: utcNow,
		Batch: &BatchState{
			BatchID:     batchID,
			Provider:    client.ProviderName(),
			Pass:        "summarize",
			SubmittedAt: utcNow,
			PathByID:    pathByID,
		},
		Pending: pending,
	}
	if err := saveBatchCheckpoint(projectDir, bcp); err != nil {
		return nil, fmt.Errorf("compile: CRITICAL — batch %s submitted but checkpoint save failed: %w (batch ID may be lost)", batchID, err)
	}

	fmt.Fprintf(os.Stderr, "\n📦 Batch submitted: %s\n", batchID)
	fmt.Fprintf(os.Stderr, "   Provider: %s\n", client.ProviderName())
	fmt.Fprintf(os.Stderr, "   Sources:  %d\n", len(requests))
	fmt.Fprintf(os.Stderr, "   Run `sage-wiki compile` again to check status and retrieve results.\n\n")

	return result, nil
}

// P1-8: deliberately NOT refactored onto setupStores — resumeBatch's
// construction lacks itemStore/tierMgr/backpressure, and verbatim reuse
// would add the chunk backfill to the batch-resume path (a behavior change).
// resumeBatch polls and retrieves a previously submitted batch, then continues the pipeline.
// The batch checkpoint (bcp) is the only resume state (P1-3); every terminal
// path retires it via retireBatchCheckpoint (strip-then-conditional-delete).
func resumeBatch(
	projectDir string,
	client *llm.Client,
	cfg *config.Config,
	mf *manifest.Manifest,
	base *manifest.Manifest,
	bcp *BatchCheckpoint,
	tracker *llm.CostTracker,
	opts CompileOpts,
) (*CompileResult, error) {
	result := &CompileResult{}
	bs := bcp.Batch

	log.Info("checking batch status", "batch_id", bs.BatchID, "provider", bs.Provider)
	status, err := client.PollBatch(bs.BatchID)
	if err != nil {
		return nil, fmt.Errorf("compile: poll batch: %w", err)
	}

	switch status.Status {
	case llm.BatchInProgress:
		fmt.Fprintf(os.Stderr, "⏳ Batch %s is still processing.\n", bs.BatchID)
		fmt.Fprintln(os.Stderr, "   Run `sage-wiki compile` again later to check.")
		return result, nil

	case llm.BatchExpired:
		log.Warn("batch expired, clearing checkpoint", "batch_id", bs.BatchID)
		fmt.Fprintf(os.Stderr, "⚠ Batch %s expired (24h window). Re-run with `compile --batch` to resubmit.\n", bs.BatchID)
		retireBatchCheckpoint(projectDir)
		return result, nil

	case llm.BatchFailed:
		log.Error("batch failed", "batch_id", bs.BatchID)
		fmt.Fprintf(os.Stderr, "✗ Batch %s failed. Re-run with `compile --batch` to resubmit.\n", bs.BatchID)
		retireBatchCheckpoint(projectDir)
		return result, nil

	case llm.BatchEnded:
		// Retrieve results below
	}

	// Retrieve batch results
	resultsRef := status.ResultsURL
	if resultsRef == "" {
		resultsRef = bs.ResultsRef
	}
	log.Info("retrieving batch results", "batch_id", bs.BatchID)
	batchResults, err := client.RetrieveBatch(resultsRef)
	if err != nil {
		return nil, fmt.Errorf("compile: retrieve batch: %w", err)
	}
	// SPEC-04 determinism: providers return batch results in arbitrary
	// order. The apply loop below feeds memStore/vecStore writes, summary
	// application, and concept extraction order — all of which must be
	// stable for byte-identical artifacts. Sort by the wire-level custom_id
	// (a stable per-document key) before any iteration.
	sort.Slice(batchResults, func(i, j int) bool { return batchResults[i].CustomID < batchResults[j].CustomID })

	// Completeness check (#124): a truncated retrieve yields a SILENTLY
	// incomplete result set, and the tail sources would vanish from this
	// compile while the checkpoint is consumed. Error out BEFORE any
	// processing — the checkpoint survives and the next compile re-polls.
	// Errored items count as returned: they are the per-item-error contract
	// (result.Errors++ below), not missing data.
	{
		expected := map[string]string{} // wire id → path (for the error message)
		legacy := bs.PathByID == nil    // same predicate the processing loop uses
		if !legacy {
			for id, path := range bs.PathByID {
				expected[id] = path
			}
		} else {
			for _, p := range bcp.Pending {
				expected[p] = p
			}
		}
		returned := map[string]bool{}
		for _, br := range batchResults {
			returned[br.CustomID] = true
		}
		matched := 0
		var missing []string
		for id, path := range expected {
			if returned[id] {
				matched++
			} else {
				missing = append(missing, path)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing) // SPEC-04 D1: deterministic error text
			return nil, fmt.Errorf("compile: batch returned %d of %d expected results — missing: %s (batch state kept for re-poll)",
				matched, len(expected), strings.Join(missing, ", "))
		}
	}

	// Open DB for indexing
	// P2-1 skip-list: import cycle (sqlitestore imports compiler) — see above.
	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("compile: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	embedder := embed.NewFromConfig(cfg)
	chunkStore := memory.NewChunkStore(db)

	progress := NewProgress()
	mfPath := filepath.Join(projectDir, ".manifest.json")

	// Build set of known pending sources for CustomID validation
	pendingSet := make(map[string]bool, len(bcp.Pending))
	for _, p := range bcp.Pending {
		pendingSet[p] = true
	}

	// Summary naming (issue #107): resolve roots once and warn on collisions
	// over the full pending set, matching the standard/on-demand path.
	batchRoots := sourceRootPaths(cfg.Sources)
	warnSummaryNameCollisions(bcp.Pending, batchRoots, cfg.Compiler.SummaryNamingOrDefault())

	// Process batch results as summaries
	progress.StartPhase("Processing batch results", len(batchResults))
	var successfulSummaries []SummaryResult

	for _, br := range batchResults {
		// Translate the wire-level custom_id back to the source path. New
		// batches (post-fix) populate bs.PathByID; legacy checkpoints written
		// before this fix have an empty map, in which case the custom_id IS
		// the path and we use it directly. Issue #89.
		path := br.CustomID
		if bs.PathByID != nil {
			if mapped, ok := bs.PathByID[br.CustomID]; ok {
				path = mapped
			} else {
				log.Warn("batch: unknown custom_id (no path mapping)", "id", br.CustomID)
				continue
			}
		}

		// Validate path matches a known pending source
		if !pendingSet[path] {
			log.Warn("batch: ignoring unknown source from batch results", "id", br.CustomID, "path", path)
			continue
		}

		if br.Error != "" {
			result.Errors++
			progress.ItemError(path, fmt.Errorf("%s", br.Error))
			continue
		}

		// Track batch usage with batch pricing, and fire the usage event
		// here — batch results arrive via polling, never through
		// client.trackUsage (SPEC-05: Pass/Tier live at this site, not in
		// batch.go).
		if br.Response != nil && tracker != nil {
			tracker.Track(bs.Pass, br.Response.Model, br.Response.Usage, true)
			client.RecordBatchUsage(orBackground(opts.Ctx), bs.Pass, br.Response.Model, br.Response.Usage)
		}

		// Guard: an empty batch summary must fail the source (no validateSummary
		// backstop on this path) rather than write a hollow summary file.
		if gErr := emptyContentError(br.Response, "batch summary", path); gErr != nil {
			result.Errors++
			progress.ItemError(path, gErr)
			continue
		}

		// Write summary file (issue #107: honor the configured naming scheme via
		// the same helper as the standard path, so batch/standard never diverge).
		summaryText := br.Response.Content
		summaryDir := filepath.Join(projectDir, cfg.Output, "summaries")
		os.MkdirAll(summaryDir, 0755)
		summaryName := SummaryFilenameMode(path, resolveSourceRoot(path, batchRoots), cfg.Compiler.SummaryNamingOrDefault())
		summaryPath := filepath.ToSlash(filepath.Join(cfg.Output, "summaries", summaryName))
		absOutputPath := filepath.Join(projectDir, summaryPath)

		frontmatter := fmt.Sprintf("---\nsource: %s\ncompiled_at: %s\nbatch: true\n---\n\n", path, timeNow(cfg.Compiler.UserTimeLocation()))
		// Canonical write-then-index order (I2): summary written atomically first.
		if err := fsutil.WriteFileAtomic(absOutputPath, []byte(frontmatter+summaryText), 0644); err != nil {
			result.Errors++
			progress.ItemError(path, err)
			continue
		}

		result.Summarized++
		progress.ItemDone(path, summaryPath)

		// Update manifest — ensure source entry exists with current resolved type,
		// then mark compiled. Refreshing Type on existing entries propagates
		// config-driven type changes on recompile.
		resolvedType := TypeForFile(projectDir, path, cfg)
		if _, exists := mf.Sources[path]; !exists {
			mf.AddSource(path, "", resolvedType, 0)
		} else {
			src := mf.Sources[path]
			src.Type = resolvedType
			mf.Sources[path] = src
		}
		mf.MarkCompiled(path, summaryPath, nil)

		// Index
		memStore.Add(memory.Entry{
			ID:          path,
			Content:     summaryText,
			Tags:        []string{resolvedType},
			ArticlePath: summaryPath,
		})
		sourcedate.RecordForSource(memStore, projectDir, path, mf.Sources[path].AddedAt)

		if embedder != nil {
			vec, err := embedder.Embed(summaryText)
			if err != nil {
				log.Warn("embedding failed", "source", path, "error", err)
			} else {
				vecStore.Upsert(path, vec)
			}
		}

		// Track for concept extraction
		successfulSummaries = append(successfulSummaries, SummaryResult{
			SourcePath:  path,
			SummaryPath: summaryPath,
			Summary:     summaryText,
		})
	}
	progress.EndPhase()

	// The batch is consumed: summaries are on disk (atomic writes above), and
	// the remaining Pass 2/3 work is re-derivable — the manifest is only saved
	// at the end of this run, so a crash re-diffs these sources and the
	// standard path reprocesses them (batch summaries lack source_hash
	// frontmatter, so they re-generate: recompute, never loss). Retire the
	// checkpoint now (strip legacy marker first; delete only on strip
	// success — spec D5).
	retireBatchCheckpoint(projectDir)

	// Continue with Pass 2 + 3 synchronously
	batchPass23OK := true // set false if extraction fails or the run is cancelled
	if len(successfulSummaries) > 0 {
		model := cfg.Models.Extract
		if model == "" {
			model = cfg.Models.Summarize
			if model == "" {
				model = "gpt-4o-mini"
			}
		}

		client.SetPass("extract")
		extCacheID, _ := client.SetupCache("You are an expert knowledge organizer. Extract structured concepts from source summaries.", model)
		progress.StartPhase("Pass 2: Extract concepts", len(successfulSummaries))
		concepts, err := ExtractConcepts(opts.Ctx, successfulSummaries, mf.Concepts, client, model, cfg.Compiler.ExtractBatchSize, cfg.Compiler.ExtractMaxTokens, cfg.Compiler.MaxParallel, opts.Prompts, cfg.Compiler.CompileTemperature())
		if err != nil {
			progress.ItemError("concept extraction", err)
			result.Errors++
			batchPass23OK = false
			progress.EndPhase()
			client.TeardownCache(extCacheID)
		} else {
			// Evidence gate (#128): source-less concepts are suppressed entirely.
			concepts, _ = filterLowEvidence(concepts, cfg.Compiler.MinConceptSourcesOrDefault())
			result.ConceptsExtracted = len(concepts)
			var conceptNames []string
			for _, c := range concepts {
				conceptNames = append(conceptNames, c.Name)
				mf.AddConcept(c.Name, filepath.ToSlash(filepath.Join(cfg.Output, "concepts", c.Name+".md")), c.Sources, c.Aliases...)
			}
			progress.ConceptsDiscovered(conceptNames)
			progress.EndPhase()
			client.TeardownCache(extCacheID)

			if len(concepts) > 0 {
				writeModel := cfg.Models.Write
				if writeModel == "" {
					writeModel = model
				}
				articleMaxTokens := cfg.Compiler.ArticleMaxTokens
				if articleMaxTokens <= 0 {
					articleMaxTokens = 4000
				}

				merged := ontology.MergedRelations(cfg.Ontology.Relations)
				mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
				ontStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
					ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
				bindOntSink(ontStore, opts.Sink, projectDir)
				client.SetPass("write")
				writeCacheID, _ := client.SetupCache("You are a knowledge base article writer. Write comprehensive, well-structured wiki articles.", writeModel)
				relPatterns := ontology.RelationPatterns(merged)
				progress.StartPhase("Pass 3: Write articles", len(concepts))
				articles := WriteArticles(ArticleWriteOpts{
					Temperature:            cfg.Compiler.CompileTemperature(),
					Prompts:                opts.Prompts,
					Ctx:                    opts.Ctx,
					ProjectDir:             projectDir,
					OutputDir:              cfg.Output,
					Client:                 client,
					Model:                  writeModel,
					MaxTokens:              articleMaxTokens,
					MaxParallel:            cfg.Compiler.MaxParallel,
					MemStore:               memStore,
					VecStore:               vecStore,
					OntStore:               ontStore,
					ChunkStore:             chunkStore,
					DB:                     db,
					Embedder:               embedder,
					UserTZ:                 cfg.Compiler.UserTimeLocation(),
					ArticleFields:          cfg.Compiler.ArticleFields,
					RelationPatterns:       relPatterns,
					ChunkSize:              cfg.Search.ChunkSizeOrDefault(),
					ChunkOverlap:           cfg.Search.ChunkOverlapOrDefault(),
					Language:               cfg.Language,
					AntiPatternPhrases:     cfg.Compiler.AntiPatternPhrasesOrDefault(),
					MaxSourceContextTokens: cfg.Compiler.MaxSourceContextTokensOrDefault(),
					AllConcepts:            manifestConceptRefs(mf.Concepts),
				}, concepts)

				for _, ar := range articles {
					if ar.Error != nil {
						result.Errors++
						progress.ItemError(ar.ConceptName, ar.Error)
					} else {
						result.ArticlesWritten++
						progress.ItemDone(ar.ConceptName, ar.ArticlePath)
					}
				}
				progress.EndPhase()
				client.TeardownCache(writeCacheID)
			}
		}
	}

	// Pass 4: Images (placeholder)
	ExtractImages(projectDir, cfg.Output, nil)

	// Community detection (P3-5): batch-resume never runs resolution, so
	// this sees the unresolved graph — documented in the spec; the next full
	// compile re-detects. Runs only when Pass 2/3 completed.
	if batchPass23OK {
		merged := ontology.MergedRelations(cfg.Ontology.Relations)
		mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		ontStore := ontology.NewStore(db, ontology.ValidRelationNames(merged), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
		bindOntSink(ontStore, opts.Sink, projectDir)
		CommunitiesPass(orBackground(opts.Ctx), projectDir, ontStore, ontStore, memStore, vecStore, embedder, cfg, client)
	}

	// If Pass 2/3 did not complete (extraction failure or cancel), this batch-resume
	// run is incomplete: skip the manifest Save entirely (below), discarding the
	// run's in-memory manifest mutations (AddSource/MarkCompiled/AddConcept) so the
	// next compile's Diff re-includes these sources and reprocesses them. The earlier
	// surgical RemoveSource dropped only the sources, leaving the run's concepts
	// orphaned (RemoveSource deletes Sources only). Mirrors the full/on-demand paths.
	// P1-1 / C1.
	if opts.Ctx != nil && opts.Ctx.Err() != nil {
		batchPass23OK = false
	}

	// Save manifest — unless the batch run was interrupted before Pass 2/3 completed.
	if !batchPass23OK {
		log.Info("batch compile interrupted before Pass 2/3 completed — manifest not saved; sources will reprocess on next compile")
	} else if err := manifest.MergeSave(orBackground(opts.Ctx), mfPath, base, mf); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	} else {
		// SPEC-04: batch compiles store keys at the same completion point as
		// standard compiles (spec edge case — Gate-1 review F-5).
		if err := storeCompileKeysForCompleted(cfg, opts.Prompts, NewCompileItemStore(db, config.NowUTC)); err != nil {
			log.Warn("compile-key storage failed (batch)", "error", err)
		}
	}

	if err := writeChangelog(projectDir, cfg.Output, result, cfg.Compiler.UserTimeLocation()); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// Check for source changes that invalidate confirmed outputs
	if cfg.Trust.IncludeOutputsMode() == "verified" {
		trustStore := trust.NewStore(db)
		batchMerged := ontology.MergedRelations(cfg.Ontology.Relations)
		batchMergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
		trustOntStore := ontology.NewStore(db, ontology.ValidRelationNames(batchMerged), ontology.ValidEntityTypeNames(batchMergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault()), ontology.WithNow(config.NowUTC))
		bindOntSink(trustOntStore, opts.Sink, projectDir)
		stores := trust.IndexStores{
			MemStore: memStore, VecStore: vecStore,
			OntStore:   trustOntStore,
			ChunkStore: chunkStore, DB: db,
		}
		demoted, err := trust.CheckSourceChanges(trustStore, projectDir, &stores)
		if err != nil {
			log.Warn("trust source check failed", "error", err)
		} else if demoted > 0 {
			log.Info("demoted stale outputs", "count", demoted)
		}
	}

	// Git auto-commit
	if cfg.Compiler.AutoCommit {
		commitMsg := fmt.Sprintf("compile (batch): +%d sources, %d concepts, %d articles",
			result.Summarized, result.ConceptsExtracted, result.ArticlesWritten)
		gitpkg.AutoCommit(projectDir, commitMsg)
	}

	progress.Summary(result)

	// Print cost report
	costReport := tracker.Report()
	if costReport.TotalTokens > 0 {
		fmt.Fprint(os.Stderr, llm.FormatReport(costReport))
		result.CostReport = costReport
	}

	return result, nil
}

func loadCompileState(path string) (*CompileState, error) {
	return loadCompileStateWith(path, os.ReadFile)
}

// loadCompileStateWith reads the checkpoint, retrying transient Windows
// file-sharing failures.
//
// The write half of this contract already retries (writeFileAtomicUnique via
// isTransientRenameError); reads had no equivalent, so a concurrent writer
// holding the handle surfaced as "The process cannot access the file because
// it is being used by another process" and aborted the caller outright —
// observed on windows-latest as a spurious abort in
// TestBatchCheckpointWriters_Concurrent. A missing file is NOT transient and
// returns immediately so callers' os.IsNotExist checks keep working.
//
// The reader is injectable so the retry is testable on any OS.
func loadCompileStateWith(path string, read func(string) ([]byte, error)) (*CompileState, error) {
	data, err := readStateFileRetrying(path, read)
	if err != nil {
		return nil, err
	}
	var state CompileState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func extractType(path string, typeSignals []config.TypeSignal) string {
	var contentHead string
	if len(typeSignals) > 0 {
		contentHead = extract.ReadHead(path, extract.DefaultHeadRunes)
	}
	return extract.DetectSourceTypeWithSignals(path, contentHead, convertSignals(typeSignals))
}

func convertSignals(typeSignals []config.TypeSignal) []extract.TypeSignal {
	signals := make([]extract.TypeSignal, len(typeSignals))
	for i, s := range typeSignals {
		signals[i] = extract.TypeSignal{
			Type:             s.Type,
			Pattern:          s.Pattern,
			FilenameKeywords: s.FilenameKeywords,
			ContentKeywords:  s.ContentKeywords,
			MinContentHits:   s.MinContentHits,
		}
	}
	return signals
}

// timeNow returns the current time in RFC3339 using the given timezone.
// Used for user-facing timestamps (frontmatter, changelog). It delegates to
// the single SDE-aware clock (config.NowUTC, SPEC-04 D4): with
// SOURCE_DATE_EPOCH set, every compile timestamp is the epoch — the seed of
// SPEC-04's deterministic artifacts and what the parity suite pins.
func timeNow(loc *time.Location) string {
	return config.NowUTC().In(loc).Format(time.RFC3339)
}

func filterSuccessful(summaries []SummaryResult) []SummaryResult {
	var result []SummaryResult
	for _, s := range summaries {
		if s.Error == nil && s.Summary != "" {
			result = append(result, s)
		}
	}
	return result
}

// hasSoleSourceOrphan returns true if removing removedPath would orphan at least
// one concept (i.e., a concept whose only source is removedPath).
func hasSoleSourceOrphan(mf *manifest.Manifest, removedPath string) bool {
	for _, name := range mf.ArticlesFromSource(removedPath) {
		if c, ok := mf.Concepts[name]; ok && len(c.Sources) <= 1 {
			return true
		}
	}
	return false
}

// handleRemovedSources processes removed source files, detecting orphaned articles
// and optionally pruning them. When prune=false and an orphan would result, ALL
// state mutations for that source are deferred to preserve recovery via later --prune.
func handleRemovedSources(projectDir string, removed []string, mf *manifest.Manifest,
	memStore store.EntryStore, vecStore store.VectorStore, ontStore store.OntologyStore, prune bool) {

	for _, removedPath := range removed {
		if !prune && hasSoleSourceOrphan(mf, removedPath) {
			log.Info("deferred source removal (orphaned concepts pending prune)",
				"path", removedPath)
			continue
		}

		affectedConcepts := mf.ArticlesFromSource(removedPath)
		for _, conceptName := range affectedConcepts {
			concept, ok := mf.Concepts[conceptName]
			if !ok {
				continue
			}
			if len(concept.Sources) <= 1 {
				log.Warn("article orphaned (sole source removed)",
					"concept", conceptName,
					"article", concept.ArticlePath,
					"source", removedPath)
				if prune {
					articleAbs := filepath.Join(projectDir, concept.ArticlePath)
					if err := os.Remove(articleAbs); err != nil && !os.IsNotExist(err) {
						log.Warn("failed to delete orphaned article", "path", articleAbs, "error", err)
					} else {
						log.Info("pruned orphaned article", "concept", conceptName, "path", concept.ArticlePath)
					}
					memStore.Delete("concept:" + conceptName)
					vecStore.Delete("concept:" + conceptName)
					ontStore.DeleteEntity(conceptName)
					delete(mf.Concepts, conceptName)
				}
			} else {
				var updated []string
				for _, s := range concept.Sources {
					if s != removedPath {
						updated = append(updated, s)
					}
				}
				concept.Sources = updated
				mf.Concepts[conceptName] = concept
				log.Info("updated concept sources (removed source)",
					"concept", conceptName, "remaining_sources", len(updated))
			}
		}

		mf.RemoveSource(removedPath)
		memStore.Delete(removedPath)
		vecStore.Delete(removedPath)
		log.Info("removed source", "path", removedPath)
	}
}

func writeChangelog(projectDir string, outputDir string, result *CompileResult, loc *time.Location) error {
	changelogPath := filepath.Join(projectDir, outputDir, "CHANGELOG.md")

	entry := fmt.Sprintf("## %s\n\n- Added: %d sources\n- Modified: %d sources\n- Removed: %d sources\n- Summarized: %d\n- Concepts extracted: %d\n- Articles written: %d\n- Errors: %d\n\n",
		timeNow(loc), result.Added, result.Modified, result.Removed,
		result.Summarized, result.ConceptsExtracted, result.ArticlesWritten, result.Errors)

	// Prepend to existing changelog
	existing, _ := os.ReadFile(changelogPath)
	header := "# CHANGELOG\n\nCompilation history for sage-wiki.\n\n"
	if len(existing) > 0 {
		content := string(existing)
		if idx := strings.Index(content, "\n## "); idx >= 0 {
			content = content[idx+1:]
		}
		return os.WriteFile(changelogPath, []byte(header+entry+content), 0644)
	}
	return os.WriteFile(changelogPath, []byte(header+entry), 0644)
}
