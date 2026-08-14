package compiler

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/pathsafe"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// SkippedDoc is a doc the compile did not recompile because its compile key
// matched (or was adopted onto) the current inputs (SPEC-04). Reason is the
// skip verdict: "unchanged" or "unchanged (adopted)".
type SkippedDoc struct {
	Path   string
	Reason string
}

// skipClassification is the outcome of the compile-key evaluation over the
// content-unchanged manifest sources (spec R0-R6).
type skipClassification struct {
	skipped []SkippedDoc
	adopted []SkippedDoc
	// drifted are unchanged-content docs whose key no longer matches — they
	// rejoin the work set (appended to diff.Modified by the caller) with
	// their pass flags reset.
	drifted []SourceInfo
	// driftReasons maps drifted path → drift class (pipeline, templates,
	// models, config, embed) for --explain/diff annotation (Task 12 reads
	// it from the stored parts; this in-memory copy feeds the compile run).
	driftReasons map[string]string
	// resume is the R0 work set itself: incomplete-flag docs appended to
	// diff.Modified by the caller so toProcess picks them up (the CLI's
	// toProcess is diff-driven; claims alone never reach fullpipeline).
	resume []SourceInfo
	// classifiedSpuriousAdded lists the manifest-untracked docs (tier<3,
	// diff-reported Added on every compile) that went through key
	// classification. The caller REMOVES them from diff.Added — otherwise
	// they double-count (Added AND Modified when drifted/resumed) and the
	// all-skip fast path can never fire for tier<3 corpora (review M1).
	classifiedSpuriousAdded []string
	// hasRow marks diff.Added paths that already have a compile_items row —
	// they are modifications (reason "content"), not new docs
	// ("content (new)").
	hasRow map[string]bool
}

// classifySkips evaluates the skip rule (spec R0-R6) over every tracked
// source that the content diff left unchanged. The iteration set is
// compile_items (all tiers), NOT the manifest — tier<3 docs never enter
// the manifest, so a manifest-driven classification would never see them.
// It needs the item store BEFORE upsertDiffItems (stored flags/keys are
// pre-run state). Unless dryRun, it persists adoptions (SetCompileKey) and
// drift/force flag resets immediately — both are resume-safe: an adopted
// key only ever asserts the current inputs, and a reset flag recompiles
// next run.
func classifySkips(
	cfg *config.Config,
	pr *prompts.Registry,
	items store.CompileItemStore,
	mf *manifest.Manifest,
	diff *DiffResult,
	force, dryRun bool,
) (*skipClassification, error) {
	return classifySkipsForMode(cfg, pr, items, mf, diff, force, dryRun, false)
}

func classifySkipsForMode(
	cfg *config.Config,
	pr *prompts.Registry,
	items store.CompileItemStore,
	mf *manifest.Manifest,
	diff *DiffResult,
	force, dryRun, injected bool,
) (*skipClassification, error) {
	out := &skipClassification{driftReasons: map[string]string{}, hasRow: map[string]bool{}}

	// One computation per run, not per doc (review M3).
	kc, err := NewKeyContextForMode(cfg, pr, injected)
	if err != nil {
		return nil, fmt.Errorf("classify: key context: %w", err)
	}

	removed := map[string]bool{}
	for _, p := range diff.Removed {
		removed[p] = true
	}
	// Manifest-untracked docs (tier<3 never enters the manifest, so Diff
	// reports them Added on every compile). The set carries each doc's FRESH
	// content hash (review N1): a key comparison made against the STORED row
	// hash silently swallows edits — the stored key always matches itself.
	// Content-changed docs are R2's domain: they stay in diff.Added, where
	// the upsert's hash-change flag reset reprocesses them.
	spuriousAdded := map[string]string{}
	for _, s := range diff.Added {
		spuriousAdded[s.Path] = s.Hash
	}
	inModified := map[string]bool{}
	for _, s := range diff.Modified {
		inModified[s.Path] = true
	}

	var rows []store.CompileItem
	for tier := 0; tier <= 3; tier++ {
		list, err := items.ListByTier(tier)
		if err != nil {
			return nil, fmt.Errorf("classify: list tier %d: %w", tier, err)
		}
		rows = append(rows, list...)
	}
	for _, item := range rows {
		out.hasRow[item.SourcePath] = true
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SourcePath < rows[j].SourcePath }) // SPEC-04 D1

	for _, item := range rows {
		path := item.SourcePath
		if removed[path] || inModified[path] {
			continue
		}
		_, inManifest := mf.Sources[path]
		_, isSpurious := spuriousAdded[path]
		if !inManifest && !isSpurious {
			// Ghost row (NEW-1): file deleted and the removal PERSISTED — a
			// stale row must never be classified, or R0 would inject the
			// deleted file into the work set on every compile (claims
			// reprocess it until dead-letter; the fast path dies). A removal
			// still deferred by the orphan rule never reaches this guard —
			// removed[] catches it first.
			continue
		}
		if isSpurious && item.Hash != spuriousAdded[path] {
			// Content changed (review N1): R2's domain — the Added flow's
			// hash-change flag reset reprocesses the doc. Never key-compare
			// against the stale stored hash.
			continue
		}
		tier := item.Tier

		// R0 — never skip an incomplete doc, and inject tier-3 LLM-pass
		// incompletes into the work set (the CLI's toProcess is diff-driven;
		// claims alone never reach fullpipeline). tier<3 incompletes are the
		// tier passes' own claim domain (retried every compile by design) —
		// injecting them would recompile permanently-failing embeds forever.
		if !tierComplete(&item) {
			llmPassesDone := item.PassSummarized && item.PassExtracted && item.PassWritten
			if item.Tier >= 3 && !llmPassesDone {
				out.resume = append(out.resume, SourceInfo{Path: path, Hash: item.Hash, Type: item.FileType, Size: item.SizeBytes})
				if _, ok := spuriousAdded[path]; ok {
					out.classifiedSpuriousAdded = append(out.classifiedSpuriousAdded, path)
				}
			}
			continue
		}

		// R1 — --force short-circuits R2-R5.
		if force {
			if !dryRun {
				if err := items.InvalidatePasses(path); err != nil {
					return nil, fmt.Errorf("force reset %s: %w", path, err)
				}
			}
			out.drifted = append(out.drifted, SourceInfo{Path: path, Hash: item.Hash, Type: item.FileType, Size: item.SizeBytes})
			out.driftReasons[path] = "forced"
			if _, ok := spuriousAdded[path]; ok {
				out.classifiedSpuriousAdded = append(out.classifiedSpuriousAdded, path)
			}
			continue
		}

		current := kc.Parts(item.Hash, tier)

		// R3 — adopt: compute + store, skip without recompiling.
		if item.CompileKey == "" {
			out.adopted = append(out.adopted, SkippedDoc{Path: path, Reason: "unchanged (adopted)"})
			if _, ok := spuriousAdded[path]; ok {
				out.classifiedSpuriousAdded = append(out.classifiedSpuriousAdded, path)
			}
			if !dryRun {
				if err := items.SetCompileKey(path, current.Key(tier), current.JSON()); err != nil {
					return nil, fmt.Errorf("adopt key %s: %w", path, err)
				}
			}
			continue
		}

		// R4 — unchanged.
		if item.CompileKey == current.Key(item.Tier) {
			out.skipped = append(out.skipped, SkippedDoc{Path: path, Reason: "unchanged"})
			if _, ok := spuriousAdded[path]; ok {
				out.classifiedSpuriousAdded = append(out.classifiedSpuriousAdded, path)
			}
			continue
		}

		// R5 — drift: reset flags, rejoin the work set.
		var stored CompileKeyParts
		class := "config"
		if err := json.Unmarshal([]byte(item.CompileKeyParts), &stored); err == nil {
			if c := DriftClass(stored, current); c != "" && c != "content" {
				class = c
			}
		} else {
			// Corrupt stored parts: fall back to the catch-all class and
			// recompile once (the new key overwrites) — the lenient story,
			// surfaced, never silent (review M10).
			log.Warn("compile: stored key parts unreadable — recompiling with config drift class", "path", path, "error", err)
		}
		if !dryRun {
			if err := items.InvalidatePasses(path); err != nil {
				return nil, fmt.Errorf("drift reset %s: %w", path, err)
			}
		}
		out.drifted = append(out.drifted, SourceInfo{Path: path, Hash: item.Hash, Type: item.FileType, Size: item.SizeBytes})
		out.driftReasons[path] = class
		if _, ok := spuriousAdded[path]; ok {
			out.classifiedSpuriousAdded = append(out.classifiedSpuriousAdded, path)
		}
	}

	return out, nil
}

// storeCompileKeysForCompleted recomputes and stores keys for every
// tier-complete row — the single storage point for docs compiled THIS run
// (spec: "set when the doc's final pass completes"). Recomputing is
// unconditional: a complete row's artifacts are current on a successful
// run, so its key must match the CURRENT inputs (empty keys adopt;
// content-drifted and config-drifted docs get their keys updated — an
// empty-only sweep would leave stale keys claiming currency forever).
// Called only on the success path: a cancelled/failed run marks nothing
// (P1-1), and a doc that failed stays tier-incomplete and untouched here.
func storeCompileKeysForCompleted(cfg *config.Config, pr *prompts.Registry, items store.CompileItemStore) error {
	return storeCompileKeysForCompletedForMode(cfg, pr, items, false)
}

func storeCompileKeysForCompletedForMode(cfg *config.Config, pr *prompts.Registry, items store.CompileItemStore, injected bool) error {
	kc, err := NewKeyContextForMode(cfg, pr, injected)
	if err != nil {
		return fmt.Errorf("store keys: key context: %w", err)
	}
	for tier := 0; tier <= 3; tier++ {
		rows, err := items.ListByTier(tier)
		if err != nil {
			return fmt.Errorf("store keys: list tier %d: %w", tier, err)
		}
		for _, item := range rows {
			if !tierComplete(&item) {
				continue
			}
			parts := kc.Parts(item.Hash, item.Tier)
			key := parts.Key(item.Tier)
			if key == item.CompileKey {
				continue // already current — no write churn
			}
			if err := items.SetCompileKey(item.SourcePath, key, parts.JSON()); err != nil {
				return fmt.Errorf("store key %s: %w", item.SourcePath, err)
			}
		}
	}
	return nil
}

// runSkipClassification opens the pre-run item store (backend-supplied when
// set, else a short-lived sqlite handle) and runs classifySkips for the
// current diff. It must run BEFORE upsertDiffItems (stored flags/keys are
// the pre-run evidence R0/R3-R5 read).
func runSkipClassification(projectDir string, run *compileRun, diff *DiffResult) (*skipClassification, error) {
	pr := run.opts.Prompts // nil = package default (overrides already loaded)
	injected := run.opts.CompletionProvider != nil
	if b := run.opts.Backend; b != nil {
		return classifySkipsForMode(run.cfg, pr, b.CompileItems(), run.mf, diff, run.opts.Force, run.opts.DryRun, injected)
	}
	sdb, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return nil, fmt.Errorf("skip classification: open db: %w", err)
	}
	defer sdb.Close()
	return classifySkipsForMode(run.cfg, pr, NewCompileItemStore(sdb, config.NowUTC), run.mf, diff, run.opts.Force, run.opts.DryRun, injected)
}

// CompileExplanation is the --explain report for one doc (spec §Observability
// / AC-5): every key component, stored-vs-current parts, and the verdict.
type CompileExplanation struct {
	Path         string          `json:"path"`
	SourceHash   string          `json:"source_hash"`
	Pipeline     string          `json:"pipeline"`
	Templates    string          `json:"templates"`
	Models       string          `json:"models"`
	ConfigHash   string          `json:"config_hash"`
	Embed        string          `json:"embed"`
	Key          string          `json:"key"`
	StoredKey    string          `json:"stored_key"`
	StoredParts  CompileKeyParts `json:"stored_parts"`
	CurrentParts CompileKeyParts `json:"current_parts"`
	Verdict      string          `json:"verdict"`
}

// ExplainCompileKey computes the --explain report for one doc, side-effect
// free (no adoptions, no resets — the skip rule as a pure query).
func ExplainCompileKey(projectDir, doc string, cfg *config.Config, pr *prompts.Registry, items store.CompileItemStore) (*CompileExplanation, error) {
	return ExplainCompileKeyForMode(projectDir, doc, cfg, pr, items, false)
}

// ExplainCompileKeyForMode computes the verdict against the caller's active
// completion mode without mutating compile state.
func ExplainCompileKeyForMode(projectDir, doc string, cfg *config.Config, pr *prompts.Registry, items store.CompileItemStore, injected bool) (*CompileExplanation, error) {
	// SPEC-08 AC1: doc is a workspace-relative name — reject traversal and
	// malformed inputs BEFORE any filesystem access, then containment-check
	// the join (pathsafe is the single containment answer).
	if err := pathsafe.ValidateRel(doc); err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}
	absPath, err := pathsafe.SafeJoin(projectDir, doc)
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}
	diskHash, err := fileHash(absPath)
	if err != nil {
		return nil, fmt.Errorf("explain %s: %w", doc, err)
	}

	item, err := items.GetByPath(doc)
	if err != nil {
		return nil, fmt.Errorf("explain %s: %w", doc, err)
	}

	ex := &CompileExplanation{Path: doc, SourceHash: diskHash, Pipeline: PipelineVersion}

	kc, err := NewKeyContextForMode(cfg, pr, injected)
	if err != nil {
		return nil, fmt.Errorf("explain %s: %w", doc, err)
	}
	if item == nil {
		// Never compiled: tier from the pipeline's own default chain —
		// tier_defaults[ext] first, then default_tier, else 1
		// (TierManager.ConfigDefault; was hardcoded 3, then a cfg-only
		// approximation — N3).
		tier := NewTierManager(&cfg.Compiler, items).ConfigDefault(doc)
		parts := kc.Parts(diskHash, tier)
		fillExplanation(ex, parts, "", tier)
		ex.Verdict = "compile: content (new)"
		return ex, nil
	}

	parts := kc.Parts(diskHash, item.Tier)
	fillExplanation(ex, parts, item.CompileKey, item.Tier)
	if err := json.Unmarshal([]byte(item.CompileKeyParts), &ex.StoredParts); err != nil && item.CompileKeyParts != "" {
		return nil, fmt.Errorf("explain %s: stored parts: %w", doc, err)
	}

	switch {
	case !tierComplete(item):
		ex.Verdict = "compile: incomplete (resume)"
	case item.Hash != diskHash:
		ex.Verdict = "compile: content"
	case item.CompileKey == "":
		ex.Verdict = "skip: unchanged (adopted)"
	case item.CompileKey == parts.Key(item.Tier):
		ex.Verdict = "skip: unchanged"
	default:
		class := DriftClass(ex.StoredParts, parts)
		if class == "" || class == "content" {
			class = "config"
		}
		ex.Verdict = "compile: " + class
	}
	return ex, nil
}

func fillExplanation(ex *CompileExplanation, parts CompileKeyParts, stored string, tier int) {
	ex.Templates = parts.Templates
	ex.Models = parts.Models
	ex.ConfigHash = parts.Config
	ex.Embed = parts.Embed
	ex.Key = parts.Key(tier)
	ex.StoredKey = stored
	ex.CurrentParts = parts
}

// populateDiffReasons writes the classification onto the DiffResult the
// caller already holds (spec §APIs): Unchanged = skipped + adopted,
// Reason per compiling entry (content / content (new) / incomplete
// (resume) / forced / drift class). diff.Reason is a SEPARATE map from
// cls.driftReasons (review F1 — mutating the shared map made
// ClassifySkipsForDiff's "drift only" return a lie).
func populateDiffReasons(diff *DiffResult, cls *skipClassification) {
	diff.Unchanged = append(append([]SkippedDoc{}, cls.skipped...), cls.adopted...)
	diff.Reason = make(map[string]string, len(cls.driftReasons)+len(cls.resume)+len(diff.Added)+len(diff.Modified))
	for path, class := range cls.driftReasons {
		diff.Reason[path] = class
	}
	for _, s := range diff.Added {
		if cls.hasRow[s.Path] {
			diff.Reason[s.Path] = "content"
		} else {
			diff.Reason[s.Path] = "content (new)"
		}
	}
	for _, s := range diff.Modified {
		if _, ok := diff.Reason[s.Path]; !ok {
			diff.Reason[s.Path] = "content"
		}
	}
	for _, s := range cls.resume {
		diff.Reason[s.Path] = "incomplete (resume)"
	}
}

// ClassifySkipsForDiff is the read-only drift surface for `sage-wiki diff`
// (SPEC-04): docs whose content is unchanged but whose compile inputs
// drifted, mapped path → drift class. No adoptions, no resets. The specced
// DiffResult fields (Unchanged, Reason — §APIs) are populated as a side
// effect so the consumer reads them from the diff it already holds.
func ClassifySkipsForDiff(cfg *config.Config, items store.CompileItemStore, mf *manifest.Manifest, diff *DiffResult) (map[string]string, error) {
	cls, err := classifySkips(cfg, nil, items, mf, diff, false, true)
	if err != nil {
		return nil, err
	}
	populateDiffReasons(diff, cls)
	return cls.driftReasons, nil
}
