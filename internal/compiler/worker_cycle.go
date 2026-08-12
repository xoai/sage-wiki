package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
)

// passHooks are the worker's tier pass functions, fields so tests can
// inject failures/panics without a live LLM (defaults are the real passes).
type passHooks struct {
	indexTier0   func(projectDir string, items []CompileItem, cr *compileRun) int
	indexTier1   func(projectDir string, items []CompileItem, cr *compileRun) (int, int)
	fullPipeline func(sources []SourceInfo, opts FullPipelineOpts) *FullPipelineResult
}

func defaultPassHooks() passHooks {
	return passHooks{
		indexTier0: func(projectDir string, items []CompileItem, cr *compileRun) int {
			return indexRawSources(projectDir, items, cr.memStore, cr.itemStore, cr.exOpts...)
		},
		indexTier1: func(projectDir string, items []CompileItem, cr *compileRun) (int, int) {
			return indexAndEmbedSources(cr.opts.Ctx, projectDir, items, cr.memStore, cr.vecStore, cr.embedder,
				cr.itemStore, cr.bp, cr.chunkStore, cr.cfg.Search.ChunkSizeOrDefault(), cr.cfg.Search.ChunkOverlapOrDefault(), cr.db, cr.exOpts...)
		},
		fullPipeline: runFullPipeline,
	}
}

// cycleRun bundles a per-cycle compileRun with its cleanup.
type cycleRun struct {
	run     *compileRun
	cleanup func()
}

// openCycleRun builds the config/manifest/store context for one worker
// cycle, reusing Compile's setupStores (spec C3 step 4: manifest load +
// merge-base clone — never loadInputs, whose fresh-clearing and
// batch-resume handoff belong to the CLI).
func (w *Worker) openCycleRun(ctx context.Context) (*cycleRun, error) {
	projectDir := w.deps.ProjectDir
	cfg, err := config.Load(filepath.Join(projectDir, "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("worker: load config: %w", err)
	}
	mfPath := filepath.Join(projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return nil, fmt.Errorf("worker: load manifest: %w", err)
	}
	mf.SetNow(config.NowUTC)
	// SPEC-07: build opts BEFORE the client so newTrackedClient's
	// recorder fallback bridges usage to the installed sink — a client
	// built with empty opts would record to the file ledger only.
	// R2: each cycle loads a FRESH registry (embedded defaults +
	// <projectDir>/prompts); a missing dir stays silent, a malformed
	// override warns and keeps defaults. run.opts.Prompts feeds both the
	// full pipeline and storeCompileKeysForCompleted below.
	pr := prompts.NewRegistry()
	if err := pr.LoadFromDir(filepath.Join(projectDir, "prompts")); err != nil {
		log.Warn("failed to load custom prompts", "error", err)
	}
	opts := CompileOpts{Ctx: ctx, Backend: w.deps.Backend, Sink: w.eventSink(), Prompts: pr}
	client, _, err := newTrackedClient(projectDir, cfg, &opts)
	if err != nil {
		return nil, fmt.Errorf("worker: create LLM client: %w", err)
	}

	run := &compileRun{
		cfg:      cfg,
		opts:     opts,
		result:   &CompileResult{},
		mf:       mf,
		mfPath:   mfPath,
		base:     mf.Clone(),
		client:   client,
		progress: w.deps.Progress,
	}
	if err := setupStores(projectDir, run); err != nil {
		return nil, fmt.Errorf("worker: setup stores: %w", err)
	}
	// The worker's queue store is authoritative for queue ops; point the
	// pass functions at the same instance so MarkPass/MarkError and
	// Claim/Release agree (spec C4: only coordinator + Progress are shared).
	if w.deps.Items != nil {
		run.itemStore = w.deps.Items
	}
	return &cycleRun{run: run, cleanup: run.closeDB}, nil
}

// processCycle is the production Process body (spec C3 steps 5-7): claim
// per tier in runTiers order, process through the existing pass functions,
// release per item. Heartbeats run for the cycle's duration.
func (w *Worker) processCycle(ctx context.Context) (bool, error) {
	cr, err := w.openCycleRun(ctx)
	if err != nil {
		return false, err
	}
	defer cr.cleanup()
	run := cr.run

	// Enqueue scan (spec C3 step 4): diff sources against the manifest,
	// upsert added/modified with resolved tiers (shared helper with the
	// CLI), drop removed sources — always prune=false: serve mode never
	// deletes article files from disk.
	diff, err := Diff(w.deps.ProjectDir, run.cfg, run.mf)
	if err != nil {
		return false, fmt.Errorf("worker: diff: %w", err)
	}
	allSources := append(diff.Added, diff.Modified...)
	upsertDiffItems(run, w.deps.ProjectDir, allSources)
	handleRemovedSources(w.deps.ProjectDir, diff.Removed, run.mf, run.memStore, run.vecStore, run.pipelineOntStore, false)
	// The queue has no file to process for a removed source — drop its row
	// BEFORE claiming (handleRemovedSources deliberately never touches
	// compile_items; leaving the row would claim a nonexistent file every
	// cycle until dead-letter). Only delete rows for sources ACTUALLY
	// removed from the manifest: a deferred sole-source orphan keeps its
	// manifest entry, and deleting its row would permanently drop it from
	// the queue if the file later returns unchanged (no diff event → no
	// re-upsert).
	var deletable []string
	for _, p := range diff.Removed {
		if _, stillKnown := run.mf.Sources[p]; !stillKnown {
			deletable = append(deletable, p)
		}
	}
	if len(deletable) > 0 {
		if err := w.deps.Items.DeleteByPaths(deletable); err != nil {
			log.Warn("worker: delete removed queue items failed", "error", err)
		}
	}

	claimed := map[int][]CompileItem{}
	paths := map[string]struct{}{}
	for _, tier := range []int{0, 1, 3} {
		items, err := w.deps.Items.Claim(tier, w.token, w.deps.Config.LeaseTTL, w.deps.Config.ClaimLimit)
		if err != nil {
			return false, fmt.Errorf("worker: claim tier %d: %w", tier, err)
		}
		claimed[tier] = items
		if len(items) > 0 {
			w.deps.Progress.QueueEvent("claimed", fmt.Sprintf("tier %d", tier), len(items))
		}
		for _, it := range items {
			paths[it.SourcePath] = struct{}{}
		}
	}
	if len(paths) == 0 {
		// No claimable work = no systemic failure — decay the backoff.
		w.failStreak = 0
		// Nothing to process — but the scan may have mutated the manifest
		// (removals), which still persists (spec C3 step 8).
		if len(diff.Removed) > 0 {
			saveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := manifest.MergeSave(saveCtx, run.mfPath, run.base, run.mf); err != nil {
				return true, fmt.Errorf("worker: save manifest: %w", err)
			}
			return true, nil
		}
		return false, nil
	}

	allPaths := make([]string, 0, len(paths))
	for p := range paths {
		allPaths = append(allPaths, p)
	}

	// SPEC-07: a worker cycle IS a compile job — bracket it. Tier reports
	// the highest claimed tier; per-doc events carry each doc's own tier.
	run.opts.JobID = events.NewID()
	cycleStart := time.Now()
	maxTier := 0
	for tier, items := range claimed {
		if len(items) > 0 && tier > maxTier {
			maxTier = tier
		}
	}
	if run.opts.Sink != nil {
		run.opts.Sink.Emit(events.NewEvent(filepath.Base(w.deps.ProjectDir), events.TypeCompileStarted, events.CompileStarted{
			JobID:    run.opts.JobID,
			Tier:     maxTier,
			DocCount: len(paths),
		}))
	}

	hbCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	go w.heartbeatLoop(hbCtx, allPaths)

	errored := map[string]bool{}
	// Items whose post-pass state could not be read: excluded from release
	// (lease expires, no budget change either way).
	readFailed := map[string]bool{}

	// Tier 0 + 1: group passes; per-item failure shows up as an ErrorCount
	// bump (index funcs MarkError per failed item).
	runPass := func(name string, items []CompileItem, pass func()) {
		if len(items) == 0 {
			return
		}
		panicked := false
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
					log.Error("worker pass panicked — items released for retry", "pass", name, "panic", r)
				}
			}()
			pass()
		}()
		for _, it := range items {
			if panicked {
				errored[it.SourcePath] = true
				continue
			}
			cur, err := w.deps.Items.GetByPath(it.SourcePath)
			if err != nil {
				// A transient store read error is NOT a processing failure:
				// the item is excluded from release entirely (its lease
				// expires and requeues) — burning no attempt budget either
				// way (retry burns; done would wrongly reset a possibly-
				// failed item's budget to 0).
				log.Warn("worker: post-pass read failed — leaving lease to expire", "path", it.SourcePath, "error", err)
				readFailed[it.SourcePath] = true
				continue
			}
			if cur == nil || cur.ErrorCount > it.ErrorCount {
				errored[it.SourcePath] = true
			}
		}
	}

	if len(claimed[0]) > 0 {
		run.progress.StartPhase("Tier 0: Index sources", len(claimed[0]))
		runPass("tier0-index", claimed[0], func() {
			w.hooks.indexTier0(w.deps.ProjectDir, claimed[0], run)
		})
		run.progress.EndPhase()
		for _, it := range claimed[0] {
			if !errored[it.SourcePath] && !readFailed[it.SourcePath] {
				run.emitDocFinished(w.deps.ProjectDir, it.SourcePath, 0)
			}
		}
	}
	if len(claimed[1]) > 0 {
		run.progress.StartPhase("Tier 1: Index + embed sources", len(claimed[1]))
		runPass("tier1-embed", claimed[1], func() {
			w.hooks.indexTier1(w.deps.ProjectDir, claimed[1], run)
		})
		run.progress.EndPhase()
		for _, it := range claimed[1] {
			if !errored[it.SourcePath] && !readFailed[it.SourcePath] {
				run.emitDocFinished(w.deps.ProjectDir, it.SourcePath, 1)
			}
		}
	}

	// Tier 3: full LLM pipeline; per-item failure = absent from
	// SucceededSources (mirrors runTiers' MarkPass rule).
	tier3Incomplete := false
	if len(claimed[3]) > 0 {
		var toProcess []SourceInfo
		for _, it := range claimed[3] {
			toProcess = append(toProcess, SourceInfo{Path: it.SourcePath, Hash: it.Hash, Type: it.FileType, Size: it.SizeBytes})
		}
		run.progress.StartPhase("Tier 3: Full compile", len(toProcess))
		panicked := false
		var pipelineResult *FullPipelineResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
					log.Error("worker pass panicked — items released for retry", "pass", "tier3-pipeline", "panic", r)
				}
			}()
			cacheEnabled := run.cfg.Compiler.PromptCacheEnabled()
			pipelineResult = w.hooks.fullPipeline(toProcess, FullPipelineOpts{
				Ctx:          ctx,
				ProjectDir:   w.deps.ProjectDir,
				Config:       run.cfg,
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
				ItemStore:    run.itemStore,
				CacheEnabled: cacheEnabled,
				Progress:     run.progress,
				Sink:         run.opts.Sink,
				Prompts:      run.opts.Prompts,
			})
		}()
		run.progress.EndPhase()

		succeeded := map[string]bool{}
		if !panicked && pipelineResult != nil && pipelineResult.Pass23Completed {
			for _, p := range pipelineResult.SucceededSources {
				succeeded[p] = true
			}
			// Commit pass flags exactly as runTiers does, or tierComplete
			// never holds and items would re-claim forever.
			for _, it := range claimed[3] {
				if !succeeded[it.SourcePath] {
					continue
				}
				for _, pass := range []string{"summarized", "extracted", "written"} {
					if err := w.deps.Items.MarkPass(it.SourcePath, pass); err != nil {
						log.Warn("worker mark pass failed", "path", it.SourcePath, "pass", pass, "error", err)
					}
				}
			}
		}
		for _, it := range claimed[3] {
			if panicked || pipelineResult == nil || !succeeded[it.SourcePath] {
				errored[it.SourcePath] = true
				continue
			}
			run.emitDocFinished(w.deps.ProjectDir, it.SourcePath, 3)
		}
		// pipelineIncomplete analogue (P1-1/C1): when Pass 2/3 did not
		// complete, the cycle's manifest mutations are discarded — the
		// next cycle re-diffs the same work from a clean state.
		tier3Incomplete = panicked || pipelineResult == nil || !pipelineResult.Pass23Completed
	}

	// SPEC-04 (review F2): the worker stores keys at the same completion
	// gate as the CLI — a serve-built workspace must not stay key-blind
	// (its first CLI compile would otherwise misreport everything as
	// "adopted" instead of the true drift class). Same P1-1 rule: an
	// incomplete cycle stores nothing.
	if !tier3Incomplete {
		if err := storeCompileKeysForCompleted(run.cfg, run.opts.Prompts, run.itemStore); err != nil {
			log.Warn("worker: compile-key storage failed", "error", err)
		}
	}

	// Release each claimed item once: errored items burn attempt budget
	// (dead-letter at cap); the rest release done — tier-complete items
	// become done, items owing more passes return to pending with the
	// budget reset (progress).
	failures := 0
	for p := range paths {
		if readFailed[p] {
			continue // lease left to expire — no release, no budget change
		}
		it := claimedItemFor(claimed, p)
		if errored[p] {
			failures++
			if it.Attempts+1 >= w.deps.Config.MaxAttempts {
				w.release(p, store.ReleaseFailed)
				w.deps.Progress.QueueEvent("dead-lettered", p, 1)
			} else {
				w.release(p, store.ReleaseRetry)
			}
		} else {
			w.release(p, store.ReleaseDone)
		}
	}

	// SPEC-07: close the cycle bracket. Outcome aligns with the
	// hibernation predicate below: a cycle with no verifiable success
	// (every item errored OR unreadable) is failed, not completed.
	outcome := "completed"
	if failures+len(readFailed) >= len(paths) {
		outcome = "failed"
	}
	// SPEC-07 metrics: worker cycles are compile jobs — they record the
	// same counter/histogram as CLI/serve-engine compiles.
	tierLabel := strconv.Itoa(maxTier)
	metrics.CounterNamed("compiles_total", "tier", tierLabel, "outcome", outcome).Add(1)
	metrics.ObserveDuration(metrics.HistogramNamed("compile_duration_seconds", metrics.CompileBuckets(), "tier", tierLabel), cycleStart)
	if run.opts.Sink != nil {
		run.opts.Sink.Emit(events.NewEvent(filepath.Base(w.deps.ProjectDir), events.TypeCompileFinished, events.CompileFinished{
			JobID:   run.opts.JobID,
			Outcome: outcome,
			Totals: events.CompileTotals{
				Docs:     len(paths),
				Compiled: len(paths) - failures - len(readFailed),
			},
		}))
	}

	// Systemic-failure hibernation: when every claimed item errored (or
	// its state became unreadable — a sick store), back the worker off
	// exponentially instead of hammering a dead backend. Counting
	// readFailed also prevents a hot re-claim loop on partial store failure.
	unaccounted := 0
	for p := range readFailed {
		if !errored[p] {
			unaccounted++
		}
	}
	if failures+unaccounted == len(paths) {
		w.failStreak++
		log.Warn("worker cycle: all claimed items failed — backing off",
			"items", len(paths), "streak", w.failStreak)
	} else {
		w.failStreak = 0
	}

	// Post-pass sweeps (spec C3 step 7, runTiers parity): serve-mode
	// items promote/demote exactly as CLI-compiled ones do.
	if run.cfg.Compiler.AutoPromoteEnabled() {
		if promoted, err := run.tierMgr.CheckPromotions(); err == nil && len(promoted) > 0 {
			for _, p := range promoted {
				fromTier := currentTier(w.deps.Items, p)
				if err := w.deps.Items.SetTier(p, 3, "auto-promote"); err != nil {
					log.Warn("worker set tier failed", "path", p, "error", err)
					continue
				}
				run.emitPromotion(w.deps.ProjectDir, p, fromTier, 3, "auto-promote")
			}
		}
	}
	if run.cfg.Compiler.AutoDemoteEnabled() {
		if demoted, err := run.tierMgr.CheckDemotions(); err == nil && len(demoted) > 0 {
			for _, p := range demoted {
				fromTier := currentTier(w.deps.Items, p)
				if err := w.deps.Items.SetTier(p, 1, "stale"); err != nil {
					log.Warn("worker set tier failed", "path", p, "error", err)
					continue
				}
				run.emitPromotion(w.deps.ProjectDir, p, fromTier, 1, "stale")
			}
		}
	}

	// Manifest persistence (spec C3 step 8): complete cycles persist under
	// the P1-2 lock; an incomplete tier-3 run discards its mutations.
	if tier3Incomplete {
		log.Info("worker: tier-3 pipeline incomplete — manifest not saved; sources reprocess next cycle")
	} else {
		// Detached ctx: a SIGINT mid-cycle must not lose a completed cycle's
		// manifest (orBackground passes a cancelled ctx through, and the
		// P1-2 lock wait would fail on it).
		saveCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := manifest.MergeSave(saveCtx, run.mfPath, run.base, run.mf); err != nil {
			return true, fmt.Errorf("worker: save manifest: %w", err)
		}
	}
	return true, nil
}

// startItemHeartbeat refreshes leases on items until the returned stop func
// is called (shared by the worker cycle and the CLI's in-process claims).
func startItemHeartbeat(items store.CompileItemStore, token string, claimed []CompileItem, interval, ttl time.Duration) func() {
	paths := make([]string, 0, len(claimed))
	for _, it := range claimed {
		paths = append(paths, it.SourcePath)
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := items.Heartbeat(token, paths, ttl); err != nil {
					log.Warn("lease heartbeat failed", "error", err)
				}
			}
		}
	}()
	return func() { close(done) }
}

// releaseClaimed settles every claimed item by outcome (shared by worker and
// CLI): errored items burn attempt budget and dead-letter at the cap; the
// rest release done (tier-complete decides done vs pending).
func releaseClaimed(items store.CompileItemStore, token string, claimed []CompileItem, errored map[string]bool, maxAttempts int) {
	for _, it := range claimed {
		var outcome store.ReleaseOutcome
		switch {
		case errored[it.SourcePath] && it.Attempts+1 >= maxAttempts:
			outcome = store.ReleaseFailed
		case errored[it.SourcePath]:
			outcome = store.ReleaseRetry
		default:
			outcome = store.ReleaseDone
		}
		if err := items.Release(it.SourcePath, token, outcome); err != nil {
			log.Warn("release claimed item failed", "path", it.SourcePath, "outcome", outcome, "error", err)
		}
	}
}

// erroredSinceClaim re-reads claimed items and reports which recorded a new
// error since the claim snapshot (index passes MarkError per failed item).
// A transient store read error is NOT a processing failure: the item is
// left out of the errored set (its lease is released done, which the
// tier-complete predicate turns into pending when passes are still owed).
func erroredSinceClaim(items store.CompileItemStore, claimed []CompileItem) map[string]bool {
	errored := map[string]bool{}
	for _, it := range claimed {
		cur, err := items.GetByPath(it.SourcePath)
		if err != nil {
			log.Warn("post-pass read failed", "path", it.SourcePath, "error", err)
			continue
		}
		if cur == nil || cur.ErrorCount > it.ErrorCount {
			errored[it.SourcePath] = true
		}
	}
	return errored
}

func claimedItemFor(claimed map[int][]CompileItem, path string) CompileItem {
	for _, items := range claimed {
		for _, it := range items {
			if it.SourcePath == path {
				return it
			}
		}
	}
	return CompileItem{SourcePath: path}
}

func (w *Worker) release(path string, outcome store.ReleaseOutcome) {
	if err := w.deps.Items.Release(path, w.token, outcome); err != nil {
		log.Warn("worker release failed", "path", path, "outcome", outcome, "error", err)
	}
}

// heartbeatLoop refreshes leases until ctx is cancelled (spec C3: the
// goroutine is independent of pass progress; TTL/heartbeat defaults give
// 4 missed-heartbeat slack).
func (w *Worker) heartbeatLoop(ctx context.Context, paths []string) {
	ticker := time.NewTicker(w.deps.Config.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.deps.Items.Heartbeat(w.token, paths, w.deps.Config.LeaseTTL); err != nil {
				log.Warn("worker heartbeat failed", "error", err)
			}
		}
	}
}
