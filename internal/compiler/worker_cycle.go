package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/store"
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
			return indexAndEmbedSources(projectDir, items, cr.memStore, cr.vecStore, cr.embedder,
				cr.itemStore, cr.bp, cr.chunkStore, cr.cfg.Search.ChunkSizeOrDefault(), cr.db, cr.exOpts...)
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
	client, _, err := newTrackedClient(cfg, &CompileOpts{})
	if err != nil {
		return nil, fmt.Errorf("worker: create LLM client: %w", err)
	}

	run := &compileRun{
		cfg:      cfg,
		opts:     CompileOpts{Ctx: ctx},
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

	claimed := map[int][]CompileItem{}
	paths := map[string]struct{}{}
	for _, tier := range []int{0, 1, 3} {
		items, err := w.deps.Items.Claim(tier, w.token, w.deps.Config.LeaseTTL, w.deps.Config.ClaimLimit)
		if err != nil {
			return false, fmt.Errorf("worker: claim tier %d: %w", tier, err)
		}
		claimed[tier] = items
		for _, it := range items {
			paths[it.SourcePath] = struct{}{}
		}
	}
	if len(paths) == 0 {
		return false, nil
	}

	allPaths := make([]string, 0, len(paths))
	for p := range paths {
		allPaths = append(allPaths, p)
	}
	hbCtx, stopHeartbeat := context.WithCancel(context.Background())
	defer stopHeartbeat()
	go w.heartbeatLoop(hbCtx, allPaths)

	errored := map[string]bool{}

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
			if err != nil || cur == nil {
				errored[it.SourcePath] = true
				continue
			}
			if cur.ErrorCount > it.ErrorCount {
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
	}
	if len(claimed[1]) > 0 {
		run.progress.StartPhase("Tier 1: Index + embed sources", len(claimed[1]))
		runPass("tier1-embed", claimed[1], func() {
			w.hooks.indexTier1(w.deps.ProjectDir, claimed[1], run)
		})
		run.progress.EndPhase()
	}

	// Tier 3: full LLM pipeline; per-item failure = absent from
	// SucceededSources (mirrors runTiers' MarkPass rule).
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
				Embedder:     run.embedder,
				Backpressure: run.bp,
				ItemStore:    run.itemStore,
				CacheEnabled: cacheEnabled,
				Progress:     run.progress,
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
			}
		}
	}

	// Release each claimed item once: errored items burn attempt budget
	// (dead-letter at cap); the rest release done — tier-complete items
	// become done, items owing more passes return to pending with the
	// budget reset (progress).
	failures := 0
	for p := range paths {
		it := claimedItemFor(claimed, p)
		if errored[p] {
			failures++
			if it.Attempts+1 >= w.deps.Config.MaxAttempts {
				w.release(p, store.ReleaseFailed)
			} else {
				w.release(p, store.ReleaseRetry)
			}
		} else {
			w.release(p, store.ReleaseDone)
		}
	}

	// Systemic-failure hibernation: when every claimed item errored, back
	// the worker off exponentially instead of hammering a dead backend.
	if failures == len(paths) {
		w.failStreak++
		log.Warn("worker cycle: all claimed items failed — backing off",
			"items", len(paths), "streak", w.failStreak)
	} else {
		w.failStreak = 0
	}
	return true, nil
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
