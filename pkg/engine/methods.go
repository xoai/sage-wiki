package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/capturefmt"
	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/export"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// DocID identifies a captured source: the source's path RELATIVE to the
// workspace root (there is no separate doc-id concept; SPEC-01 B.1 8d).
// All three capture modes return the same shape.
type DocID string

// Source is one document to capture. Exactly one of Path, URL, or Reader
// must be set.
type Source struct {
	Path   string    // file on disk (copied into the workspace sources)
	URL    string    // downloaded as markdown
	Reader io.Reader // raw content, filed under raw/captures/
	Type   string    // optional label for Reader captures
	// Origin, Tags, and Context are the capture frontmatter (Reader only):
	// the single capture-file format shared by the CLI and API callers
	// (pack rule 2 — one implementation per behavior).
	Origin  string // default "capture"; the CLI passes "cli-capture"
	Tags    string // comma-separated, recorded as a YAML list
	Context string // free-text capture context
}

// Capture ingests one document and returns its id.
func (w *Workspace) Capture(ctx context.Context, src Source) (DocID, error) {
	ctx = orBackground(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkMutable(); err != nil {
		return "", err
	}

	set := 0
	for _, b := range []bool{src.Path != "", src.URL != "", src.Reader != nil} {
		if b {
			set++
		}
	}
	if set != 1 {
		return "", fmt.Errorf("engine: exactly one of Source.Path, Source.URL, Source.Reader must be set")
	}

	switch {
	case src.Path != "":
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res, err := wiki.IngestPath(w.dir, src.Path)
		if err != nil {
			return "", fmt.Errorf("engine: capture path: %w", err)
		}
		return DocID(res.SourcePath), nil

	case src.URL != "":
		if err := ctx.Err(); err != nil {
			return "", err
		}
		res, err := wiki.IngestURL(w.dir, src.URL)
		if err != nil {
			return "", fmt.Errorf("engine: capture url: %w", err)
		}
		return DocID(res.SourcePath), nil

	default:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		capturesDir := filepath.Join(w.dir, "raw", "captures")
		if err := os.MkdirAll(capturesDir, 0o755); err != nil {
			return "", fmt.Errorf("engine: create captures dir: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(src.Reader, maxCaptureBytes+1))
		if err != nil {
			return "", fmt.Errorf("engine: read capture: %w", err)
		}
		if int64(len(data)) > maxCaptureBytes {
			return "", fmt.Errorf("%w: capture exceeds %d bytes", ErrDocTooLarge, maxCaptureBytes)
		}

		// The ONE capture-file format (pack rule 2): date-slug name with
		// -N dedup, frontmatter from the shared capturefmt builder.
		now := w.app.Config.Compiler.UserNow()
		origin := src.Origin
		if origin == "" {
			origin = "capture"
		}
		fm, err := capturefmt.Frontmatter(origin, now, src.Tags, src.Context)
		if err != nil {
			return "", fmt.Errorf("engine: %w", err)
		}

		slug := "capture-" + now[:10]
		if src.Type != "" {
			slug = "capture-" + src.Type + "-" + now[:10]
		}
		dst := filepath.Join(capturesDir, slug+".md")
		for i := 1; ; i++ {
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				break
			}
			dst = filepath.Join(capturesDir, fmt.Sprintf("%s-%d.md", slug, i))
		}
		if err := os.WriteFile(dst, []byte(fm+string(data)+"\n"), 0o644); err != nil {
			return "", fmt.Errorf("engine: write capture: %w", err)
		}
		rel, err := filepath.Rel(w.dir, dst)
		if err != nil {
			return "", fmt.Errorf("engine: relativize capture path: %w", err)
		}
		return DocID(rel), nil
	}
}

const maxCaptureBytes = 50 * 1024 * 1024

// TierUseConfig leaves the compile tier at the workspace's configured
// default in a CompileRequest.
const TierUseConfig = -1

// CompileRequest selects what to compile and the guards for the run.
type CompileRequest struct {
	// Selector chooses sources. v1 supports "pending" (the diff set);
	// anything else returns an error.
	Selector string
	// Tier overrides the compile tier for this run (-1 = config default;
	// 0..3 per the tiered-compilation model).
	Tier int
	// Model is a model hint overriding the configured models for this run.
	Model string
	// MaxDocs caps the number of sources compiled (0 = no cap).
	MaxDocs int
	// MaxCost stops the run between passes once accumulated cost exceeds
	// it. nil = no guard. Exceeding returns a partial CompileResult with
	// ErrBudgetExceeded.
	MaxCost *decimal.Decimal

	// Run-mode flags (map onto the compiler's options).
	DryRun  bool
	Fresh   bool // ignore checkpoint
	Batch   bool // batch API
	NoCache bool // disable prompt caching
	Prune   bool // delete orphaned articles
}

// CompileResult mirrors the compiler's result with the SPEC-05 cost shape:
// Cost is nil when unknown — never a fabricated zero. Field names match
// compiler.CompileResult so JSON output stays shape-compatible (the cost
// portion intentionally evolves to the SPEC-01 Money shape: cost +
// assumptions + unknown_models replace cost_report).
type CompileResult struct {
	Added, Modified, Removed                int
	Summarized, ConceptsExtracted           int
	ArticlesWritten, Errors, EmbedErrors    int
	TierIndexed, TierEmbedded, TierCompiled int
	Cost                                    *decimal.Decimal `json:"cost"`
	Assumptions                             []string         `json:"assumptions,omitempty"`
	UnknownModels                           []string         `json:"unknown_models,omitempty"`
}

// Compile runs the pipeline over the pending diff.
func (w *Workspace) Compile(ctx context.Context, req CompileRequest) (*CompileResult, error) {
	ctx = orBackground(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkMutable(); err != nil {
		return nil, err
	}
	if req.Selector != "" && req.Selector != "pending" {
		return nil, fmt.Errorf("engine: selector %q unsupported (v1 supports \"pending\")", req.Selector)
	}
	if req.Tier < TierUseConfig || req.Tier > 3 {
		return nil, fmt.Errorf("engine: tier %d out of range (%d..3)", req.Tier, TierUseConfig)
	}
	// F-051: the budget guard lives in the full pipeline's pass boundaries;
	// batch has no pass-boundary hook — reject the combination honestly
	// rather than run unguarded.
	if req.Batch && req.MaxCost != nil {
		return nil, fmt.Errorf("engine: MaxCost is not supported with Batch (guard runs between passes; batch has no pass boundary)")
	}

	var tierOverride *int
	if req.Tier >= 0 {
		tierOverride = &req.Tier
	}
	res, err := compiler.Compile(w.dir, compiler.CompileOpts{
		Ctx:      ctx,
		DryRun:   req.DryRun,
		Fresh:    req.Fresh,
		Batch:    req.Batch,
		NoCache:  req.NoCache,
		Prune:    req.Prune,
		Tier:     tierOverride,
		Model:    req.Model,
		MaxDocs:  req.MaxDocs,
		MaxCost:  req.MaxCost,
		Prompts:  w.prompts,
		Recorder: w.usageRecorder(),
	})
	if err != nil && !errors.Is(err, compiler.ErrBudgetExceeded) {
		return mapCompileResult(res), err
	}
	if errors.Is(err, compiler.ErrBudgetExceeded) {
		return mapCompileResult(res), fmt.Errorf("%w (partial result returned)", ErrBudgetExceeded)
	}
	return mapCompileResult(res), nil
}

func mapCompileResult(res *compiler.CompileResult) *CompileResult {
	if res == nil {
		return &CompileResult{}
	}
	out := &CompileResult{
		Added: res.Added, Modified: res.Modified, Removed: res.Removed,
		Summarized: res.Summarized, ConceptsExtracted: res.ConceptsExtracted,
		ArticlesWritten: res.ArticlesWritten, Errors: res.Errors, EmbedErrors: res.EmbedErrors,
		TierIndexed: res.TierIndexed, TierEmbedded: res.TierEmbedded, TierCompiled: res.TierCompiled,
	}
	if res.CostReport != nil {
		out.Cost = res.CostReport.Cost
		out.Assumptions = res.CostReport.Assumptions
		out.UnknownModels = res.CostReport.UnknownModels
	}
	return out
}

// WorkspaceStats mirrors the wiki status surface.
type WorkspaceStats struct {
	SourceCount, PendingCount, ConceptCount int
	EntryCount, VectorCount, VectorDims     int
	EntityCount, RelationCount              int
	TierDistribution                        map[int]int
}

// Stats collects workspace statistics.
func (w *Workspace) Stats(ctx context.Context) (WorkspaceStats, error) {
	ctx = orBackground(ctx)
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.checkOpen(); err != nil {
		return WorkspaceStats{}, err
	}
	if err := ctx.Err(); err != nil {
		return WorkspaceStats{}, err
	}
	info, err := wiki.GetStatus(w.dir, &wiki.Stores{
		Mem: w.app.Mem,
		Vec: w.app.Vec,
		Ont: w.app.Ont,
		DB:  w.app.DB,
	})
	if err != nil {
		return WorkspaceStats{}, fmt.Errorf("engine: stats: %w", err)
	}
	return WorkspaceStats{
		SourceCount: info.SourceCount, PendingCount: info.PendingCount,
		ConceptCount: info.ConceptCount, EntryCount: info.EntryCount,
		VectorCount: info.VectorCount, VectorDims: info.VectorDims,
		EntityCount: info.EntityCount, RelationCount: info.RelationCount,
		TierDistribution: info.TierDistribution,
	}, nil
}

// Export writes a tar of the workspace directory to dst.
func (w *Workspace) Export(ctx context.Context, dst io.Writer) error {
	ctx = orBackground(ctx)
	w.mu.RLock()
	defer w.mu.RUnlock()
	if err := w.checkOpen(); err != nil {
		return err
	}
	// Shared deterministic exporter (SPEC-04 D5): normalized headers,
	// symlinks and engine.lock skipped. The live DB caveat from the pre-D5
	// implementation is unchanged (documented in internal/export).
	return export.Tar(ctx, w.dir, dst)
}
