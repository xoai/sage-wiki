package engine

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// DocID identifies a captured source. Today it is the source's path inside
// the workspace (there is no separate doc-id concept; SPEC-01 B.1 8d).
type DocID string

// Source is one document to capture. Exactly one of Path, URL, or Reader
// must be set.
type Source struct {
	Path   string    // file on disk (copied into the workspace sources)
	URL    string    // downloaded as markdown
	Reader io.Reader // raw content, filed under raw/captures/
	Type   string    // optional label for Reader captures
}

// Capture ingests one document and returns its id.
func (w *Workspace) Capture(ctx context.Context, src Source) (DocID, error) {
	if err := w.checkMutable(); err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()

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
		name := fmt.Sprintf("capture-%d.md", time.Now().Unix())
		if src.Type != "" {
			name = fmt.Sprintf("capture-%s-%d.md", src.Type, time.Now().Unix())
		}
		dst := filepath.Join(capturesDir, name)
		data, err := io.ReadAll(io.LimitReader(src.Reader, maxCaptureBytes+1))
		if err != nil {
			return "", fmt.Errorf("engine: read capture: %w", err)
		}
		if int64(len(data)) > maxCaptureBytes {
			return "", fmt.Errorf("%w: capture exceeds %d bytes", ErrDocTooLarge, maxCaptureBytes)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return "", fmt.Errorf("engine: write capture: %w", err)
		}
		return DocID(dst), nil
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
}

// CompileResult mirrors the compiler's result with the SPEC-05 cost shape:
// Cost is nil when unknown — never a fabricated zero.
type CompileResult struct {
	Added, Modified, Removed           int
	Summarized, Concepts, Articles     int
	Errors, EmbedErrors                int
	TierIndexed, TierEmbedded, TierCompiled int
	Cost                               *decimal.Decimal
	Assumptions                        []string
	UnknownModels                      []string
}

// Compile runs the pipeline over the pending diff.
func (w *Workspace) Compile(ctx context.Context, req CompileRequest) (*CompileResult, error) {
	if err := w.checkMutable(); err != nil {
		return nil, err
	}
	if req.Selector != "" && req.Selector != "pending" {
		return nil, fmt.Errorf("engine: selector %q unsupported (v1 supports \"pending\")", req.Selector)
	}
	if req.Tier < TierUseConfig || req.Tier > 3 {
		return nil, fmt.Errorf("engine: tier %d out of range (%d..3)", req.Tier, TierUseConfig)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var tierOverride *int
	if req.Tier >= 0 {
		tierOverride = &req.Tier
	}
	res, err := compiler.Compile(w.dir, compiler.CompileOpts{
		Ctx:      ctx,
		Tier:     tierOverride,
		Model:    req.Model,
		MaxDocs:  req.MaxDocs,
		MaxCost:  req.MaxCost,
		Prompts:  w.prompts,
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
		Summarized: res.Summarized, Concepts: res.ConceptsExtracted,
		Articles: res.ArticlesWritten, Errors: res.Errors, EmbedErrors: res.EmbedErrors,
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
	if err := w.checkOpen(); err != nil {
		return WorkspaceStats{}, err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
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
	if err := w.checkOpen(); err != nil {
		return err
	}
	w.mu.RLock()
	defer w.mu.RUnlock()

	tw := tar.NewWriter(dst)
	walkErr := filepath.WalkDir(w.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == w.dir {
			return nil
		}
		rel, err := filepath.Rel(w.dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err := tw.Close(); walkErr == nil {
		walkErr = err
	}
	return walkErr
}
