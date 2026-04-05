package compiler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/extract"
	gitpkg "github.com/xoai/sage-wiki/internal/git"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// CompileOpts configures a compilation run.
type CompileOpts struct {
	DryRun bool
	Fresh  bool // ignore checkpoint
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
}

// CompileState tracks progress for checkpoint/resume (ADR-018).
type CompileState struct {
	CompileID string         `json:"compile_id"`
	StartedAt string         `json:"started_at"`
	Pass      int            `json:"pass"`
	Completed []string       `json:"completed"`
	Pending   []string       `json:"pending"`
	Failed    []FailedSource `json:"failed,omitempty"`
}

type FailedSource struct {
	Path     string `json:"path"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

// Compile runs Pass 0 (diff) and Pass 1 (summarize) of the compiler pipeline.
func Compile(projectDir string, opts CompileOpts) (*CompileResult, error) {
	result := &CompileResult{}

	// Load config
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("compile: load config: %w", err)
	}

	// Load manifest
	mfPath := filepath.Join(projectDir, ".manifest.json")
	mf, err := manifest.Load(mfPath)
	if err != nil {
		return nil, fmt.Errorf("compile: load manifest: %w", err)
	}

	// Check for existing checkpoint
	statePath := filepath.Join(projectDir, ".sage", "compile-state.json")
	var state *CompileState
	if !opts.Fresh {
		state, _ = loadCompileState(statePath)
		if state != nil {
			log.Info("resuming from checkpoint", "compile_id", state.CompileID, "pass", state.Pass, "completed", len(state.Completed))
		}
	}

	// Pass 0: Diff
	log.Info("Pass 0: computing diff")
	diff, err := Diff(projectDir, cfg, mf)
	if err != nil {
		return nil, fmt.Errorf("compile: diff: %w", err)
	}

	result.Added = len(diff.Added)
	result.Modified = len(diff.Modified)
	result.Removed = len(diff.Removed)

	if result.Added == 0 && result.Modified == 0 && result.Removed == 0 {
		log.Info("nothing to compile")
		return result, nil
	}

	if opts.DryRun {
		log.Info("dry run — showing changes only")
		for _, s := range diff.Added {
			fmt.Printf("  + %s (%s)\n", s.Path, s.Type)
		}
		for _, s := range diff.Modified {
			fmt.Printf("  ~ %s (%s)\n", s.Path, s.Type)
		}
		for _, p := range diff.Removed {
			fmt.Printf("  - %s\n", p)
		}
		return result, nil
	}

	// Create LLM client
	client, err := llm.NewClient(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.API.RateLimit)
	if err != nil {
		return nil, fmt.Errorf("compile: create LLM client: %w", err)
	}

	// Open DB
	dbPath := filepath.Join(projectDir, ".sage", "wiki.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("compile: open db: %w", err)
	}
	defer db.Close()

	memStore := memory.NewStore(db)
	vecStore := vectors.NewStore(db)
	embedder := embed.NewForConfig(cfg)

	// Initialize checkpoint state
	if state == nil {
		state = &CompileState{
			CompileID: time.Now().Format("20060102-150405"),
			StartedAt: timeNow(),
			Pass:      1,
		}
		for _, s := range diff.Added {
			state.Pending = append(state.Pending, s.Path)
		}
		for _, s := range diff.Modified {
			state.Pending = append(state.Pending, s.Path)
		}
	}

	// Filter to only pending sources (for resume)
	pendingSet := make(map[string]bool)
	for _, p := range state.Pending {
		pendingSet[p] = true
	}

	var toProcess []SourceInfo
	for _, s := range append(diff.Added, diff.Modified...) {
		if pendingSet[s.Path] {
			toProcess = append(toProcess, s)
		}
	}

	// Pass 1: Summarize (skip if checkpoint says Pass 1 already done)
	if state.Pass > 1 {
		log.Info("Pass 1: skipping (checkpoint indicates already complete)")
	}
	if state.Pass <= 1 && len(toProcess) > 0 {
		state.Pass = 1
	}
	log.Info("Pass 1: summarizing sources", "count", len(toProcess))

	model := cfg.Models.Summarize
	if model == "" {
		model = "gpt-4o-mini"
	}
	maxTokens := cfg.Compiler.SummaryMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2000
	}

	summaries := Summarize(projectDir, cfg.Output, toProcess, client, model, maxTokens, cfg.Compiler.MaxParallel)

	for _, sr := range summaries {
		if sr.Error != nil {
			result.Errors++
			state.Failed = append(state.Failed, FailedSource{
				Path:  sr.SourcePath,
				Error: sr.Error.Error(),
			})
			continue
		}

		result.Summarized++

		// Update manifest
		src := mf.Sources[sr.SourcePath]
		mf.MarkCompiled(sr.SourcePath, sr.SummaryPath, sr.Concepts)

		// Register source if new
		if src.Hash == "" {
			for _, s := range toProcess {
				if s.Path == sr.SourcePath {
					mf.AddSource(s.Path, s.Hash, s.Type, s.Size)
					mf.MarkCompiled(s.Path, sr.SummaryPath, sr.Concepts)
					break
				}
			}
		}

		// Index in FTS5
		memStore.Add(memory.Entry{
			ID:          sr.SourcePath,
			Content:     sr.Summary,
			Tags:        []string{extractType(sr.SourcePath)},
			ArticlePath: sr.SummaryPath,
		})

		// Generate embedding
		if embedder != nil {
			vec, err := embedder.Embed(sr.Summary)
			if err != nil {
				log.Warn("embedding failed", "source", sr.SourcePath, "error", err)
			} else {
				vecStore.Upsert(sr.SourcePath, vec)
			}
		}

		// Update checkpoint
		removeFromPending(state, sr.SourcePath)
		state.Completed = append(state.Completed, sr.SourcePath)
		saveCompileState(statePath, state)
	}

	// Update checkpoint pass
	state.Pass = 2
	saveCompileState(statePath, state)

	// Pass 2: Concept extraction
	successfulSummaries := filterSuccessful(summaries)
	if len(successfulSummaries) > 0 {
		extractModel := cfg.Models.Extract
		if extractModel == "" {
			extractModel = model
		}

		log.Info("Pass 2: extracting concepts", "from_summaries", len(successfulSummaries))
		concepts, err := ExtractConcepts(successfulSummaries, mf.Concepts, client, extractModel)
		if err != nil {
			log.Error("concept extraction failed", "error", err)
			result.Errors++
		} else {
			result.ConceptsExtracted = len(concepts)

			// Update manifest with concepts
			for _, c := range concepts {
				mf.AddConcept(c.Name, filepath.Join(cfg.Output, "concepts", c.Name+".md"), c.Sources)
			}

			// Pass 3: Write articles
			if len(concepts) > 0 {
				writeModel := cfg.Models.Write
				if writeModel == "" {
					writeModel = model
				}
				articleMaxTokens := cfg.Compiler.ArticleMaxTokens
				if articleMaxTokens <= 0 {
					articleMaxTokens = 4000
				}

				ontStore := ontology.NewStore(db)

				log.Info("Pass 3: writing articles", "concepts", len(concepts))
				articles := WriteArticles(projectDir, cfg.Output, concepts, client, writeModel, articleMaxTokens, cfg.Compiler.MaxParallel, memStore, vecStore, ontStore, embedder)

				for _, ar := range articles {
					if ar.Error != nil {
						result.Errors++
					} else {
						result.ArticlesWritten++
					}
				}
			}
		}
	}

	// Pass 4: Image extraction (placeholder)
	ExtractImages(projectDir, cfg.Output, toProcess)

	// Handle removed sources
	for _, removed := range diff.Removed {
		mf.RemoveSource(removed)
		memStore.Delete(removed)
		vecStore.Delete(removed)
		log.Info("removed source", "path", removed)
	}

	// Save manifest
	if err := mf.Save(mfPath); err != nil {
		return nil, fmt.Errorf("compile: save manifest: %w", err)
	}

	// Write CHANGELOG entry
	if err := writeChangelog(projectDir, cfg.Output, result); err != nil {
		log.Warn("failed to write CHANGELOG", "error", err)
	}

	// Clean up checkpoint on success
	if result.Errors == 0 {
		os.Remove(statePath)
	} else {
		saveCompileState(statePath, state)
	}

	// Git auto-commit
	if cfg.Compiler.AutoCommit {
		commitMsg := fmt.Sprintf("compile: +%d sources, %d concepts, %d articles",
			result.Added, result.ConceptsExtracted, result.ArticlesWritten)
		gitpkg.AutoCommit(projectDir, commitMsg)
	}

	log.Info("compilation complete",
		"added", result.Added,
		"modified", result.Modified,
		"removed", result.Removed,
		"summarized", result.Summarized,
		"concepts", result.ConceptsExtracted,
		"articles", result.ArticlesWritten,
		"errors", result.Errors,
	)

	return result, nil
}

func loadCompileState(path string) (*CompileState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state CompileState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveCompileState(path string, state *CompileState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeFromPending(state *CompileState, path string) {
	for i, p := range state.Pending {
		if p == path {
			state.Pending = append(state.Pending[:i], state.Pending[i+1:]...)
			return
		}
	}
}

func extractType(path string) string {
	return extract.DetectSourceType(path)
}

func timeNow() string {
	return time.Now().UTC().Format(time.RFC3339)
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

func writeChangelog(projectDir string, outputDir string, result *CompileResult) error {
	changelogPath := filepath.Join(projectDir, outputDir, "CHANGELOG.md")

	entry := fmt.Sprintf("## %s\n\n- Added: %d sources\n- Modified: %d sources\n- Removed: %d sources\n- Summarized: %d\n- Concepts extracted: %d\n- Articles written: %d\n- Errors: %d\n\n",
		timeNow(), result.Added, result.Modified, result.Removed,
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
