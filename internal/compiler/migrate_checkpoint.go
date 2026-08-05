package compiler

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/store"
	"sort"
)

// MigrateCheckpoint migrates compile-state.json into compile_items table.
// It reads the existing JSON checkpoint, populates compile_items with state
// from both the checkpoint and the manifest, then deletes the JSON file.
//
// Batch state is NOT migrated here (P1-3): loadOrMigrateBatchCheckpoint
// splits an in-flight batch into .sage/batch-state.json at the batch-resume
// check, long before this runs, so a Batch != nil here is unreachable in
// practice. The defensive strip below is belt-and-braces, and it is
// load-bearing rather than redundant with the end-of-function delete: the
// Upsert error returns between them skip the delete, and without the strip
// a consumed-batch marker would survive to re-materialize batch-state.json
// on the next run.
//
// Returns true if migration was performed, false if skipped or not needed.
func MigrateCheckpoint(projectDir string, items store.CompileItemStore, mf *manifest.Manifest, cfg *config.Config) (bool, error) {
	statePath := legacyCheckpointPath(projectDir)
	state, err := loadCompileState(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // no checkpoint to migrate
		}
		return false, err
	}

	if state.Batch != nil {
		log.Warn("legacy checkpoint still has in-flight batch at migration — stripping defensively (batch split should have run first)",
			"batch_id", state.Batch.BatchID, "provider", state.Batch.Provider)
		if err := stripLegacyBatch(projectDir); err != nil {
			log.Warn("defensive batch strip failed", "error", err)
		}
	}

	completedSet := make(map[string]bool)
	for _, p := range state.Completed {
		completedSet[p] = true
	}
	failedSet := make(map[string]FailedSource)
	for _, f := range state.Failed {
		failedSet[f.Path] = f
	}

	migrated := 0

	// Migrate all sources from manifest into compile_items (SPEC-04 D1:
	// sorted — the inserted rowids are permanent).
	paths := make([]string, 0, len(mf.Sources))
	for path := range mf.Sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		src := mf.Sources[path]
		item := CompileItem{
			SourcePath:  path,
			Hash:        src.Hash,
			FileType:    src.Type,
			SizeBytes:   src.SizeBytes,
			Tier:        resolveTierDefault(path, cfg),
			TierDefault: resolveTierDefault(path, cfg),
			SourceType:  "compiler",
			CompileID:   state.CompileID,
		}

		if src.SummaryPath != "" {
			item.SummaryPath = src.SummaryPath
		}

		// Sources with status "compiled" have completed all passes
		if src.Status == "compiled" {
			item.Tier = 3
			item.PassIndexed = true
			item.PassEmbedded = true
			item.PassSummarized = true
			item.PassExtracted = true
			item.PassWritten = true
		} else if completedSet[path] {
			// In the checkpoint's completed list — mark passes based on checkpoint pass level
			item.PassIndexed = true
			item.PassEmbedded = true
			if state.Pass >= 1 {
				item.PassSummarized = true
			}
			if state.Pass >= 2 {
				item.PassExtracted = true
			}
			if state.Pass >= 3 {
				item.PassWritten = true
			}
		}

		// Record errors from checkpoint
		if failed, ok := failedSet[path]; ok {
			item.Error = failed.Error
			item.ErrorCount = failed.Attempts
		}

		if err := items.Upsert(item); err != nil {
			return false, err
		}
		migrated++
	}

	// Also migrate pending sources that might not be in manifest yet
	for _, p := range state.Pending {
		if _, exists := mf.Sources[p]; exists {
			continue // already handled above
		}
		item := CompileItem{
			SourcePath:  p,
			Tier:        1, // default for unknown sources
			TierDefault: 1,
			SourceType:  "compiler",
			CompileID:   state.CompileID,
		}
		if err := items.Upsert(item); err != nil {
			return false, err
		}
		migrated++
	}

	// Delete the JSON checkpoint
	if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
		log.Warn("failed to remove compile-state.json", "error", err)
	}

	log.Info("checkpoint migrated to compile_items",
		"sources", migrated, "completed", len(state.Completed),
		"pending", len(state.Pending), "failed", len(state.Failed))

	return true, nil
}

// resolveTierDefault returns the default tier for a source path based on config.
// Uses file extension (not semantic type) to match tier_defaults, consistent
// with TierManager.ConfigDefault.
func resolveTierDefault(sourcePath string, cfg *config.Config) int {
	ext := strings.TrimPrefix(filepath.Ext(sourcePath), ".")
	if cfg.Compiler.TierDefaults != nil {
		if tier, ok := cfg.Compiler.TierDefaults[ext]; ok {
			return tier
		}
	}
	if cfg.Compiler.DefaultTier > 0 {
		return cfg.Compiler.DefaultTier
	}
	return 1 // default
}

// PopulateFromManifest creates compile_items entries for all manifest sources
// that don't already exist in compile_items. Used on first run after migration V5
// when there is no compile-state.json to migrate.
func PopulateFromManifest(items store.CompileItemStore, mf *manifest.Manifest, cfg *config.Config) (int, error) {
	populated := 0

	// SPEC-04 determinism: mf.Sources is a map — iterating it directly
	// upserts compile_items (and thus .sage/wiki.db) in randomized order.
	// Collect and sort the paths first so the migration is byte-stable.
	paths := make([]string, 0, len(mf.Sources))
	for path := range mf.Sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		src := mf.Sources[path]
		// Skip if already exists
		existing, err := items.GetByPath(path)
		if err != nil {
			return populated, err
		}
		if existing != nil {
			continue
		}

		item := CompileItem{
			SourcePath:  path,
			Hash:        src.Hash,
			FileType:    src.Type,
			SizeBytes:   src.SizeBytes,
			Tier:        resolveTierDefault(path, cfg),
			TierDefault: resolveTierDefault(path, cfg),
			SourceType:  "compiler",
		}

		if src.SummaryPath != "" {
			item.SummaryPath = src.SummaryPath
		}

		if src.Status == "compiled" {
			item.Tier = 3
			item.PassIndexed = true
			item.PassEmbedded = true
			item.PassSummarized = true
			item.PassExtracted = true
			item.PassWritten = true
		}

		if err := items.Upsert(item); err != nil {
			return populated, err
		}
		populated++
	}

	return populated, nil
}
