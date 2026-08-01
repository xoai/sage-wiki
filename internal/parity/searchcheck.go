package parity

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// SearchQuery is one committed parity query.
type SearchQuery struct {
	Q        string   `json:"q"`
	Channels []string `json:"channels,omitempty"`
	Expect   []Expect `json:"expect"`
}

// Expect is one expected result row (exact score, no epsilons).
type Expect struct {
	Doc   string  `json:"doc"`
	Rank  int     `json:"rank"`
	Score float64 `json:"score"`
}

// SearchGolden is the search.json schema.
type SearchGolden struct {
	GoldenFormatVersion int           `json:"golden_format_version"`
	Queries             []SearchQuery `json:"queries"`
}

// fnvEmbedder is the deterministic in-process embedder for search (same
// fnv scheme + 8 dims as the scripted origin, so query vectors are
// consistent with the compile-time vectors stored in the DB).
type fnvEmbedder struct{}

// Embed implements embed.Embedder.
func (fnvEmbedder) Embed(text string) ([]float32, error) { return fnvVec(text, 8), nil }

// Dimensions implements embed.Embedder.
func (fnvEmbedder) Dimensions() int { return 8 }

// Name implements embed.Embedder.
func (fnvEmbedder) Name() string { return "parity-fnv" }

// searchDeps builds search.Deps directly from the workspace (harness is
// in-module; engine's unexported app is not needed — F-013's mechanism).
// vecOpts are extra vectors.Store options (SPEC-06: the mmap backend's
// golden run passes WithVectorBackend/WithIndexDir; default callers pass
// nothing and get byte-identical behavior).
func searchDeps(wsDir string, vecOpts ...vectors.Option) (search.Deps, *storage.DB, error) {
	cfg, err := config.Load(filepath.Join(wsDir, "config.yaml"))
	if err != nil {
		return search.Deps{}, nil, fmt.Errorf("load config: %w", err)
	}
	db, err := storage.Open(filepath.Join(wsDir, ".sage", "wiki.db"))
	if err != nil {
		return search.Deps{}, nil, fmt.Errorf("open db: %w", err)
	}
	mergedRels := ontology.MergedRelations(cfg.Ontology.Relations)
	mergedTypes := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	deps := search.Deps{
		Mem:          memory.NewStore(db),
		Chunks:       memory.NewChunkStore(db),
		Vec:          vectors.NewStore(db, append([]vectors.Option{vectors.WithANN(cfg.Search.ANNEnabled())}, vecOpts...)...),
		Embedder:     fnvEmbedder{},
		Model:        cfg.Models.Query,
		BM25Weight:   cfg.Search.HybridWeightBM25,
		VectorWeight: cfg.Search.HybridWeightVector,
		Ont: ontology.NewStore(db, ontology.ValidRelationNames(mergedRels), ontology.ValidEntityTypeNames(mergedTypes),
			ontology.WithTemporalEnabled(cfg.Ontology.Temporal.EnabledOrDefault())),
		GraphWeight: cfg.Search.HybridWeightGraph,
		Now:         goldenNow,
	}
	if deps.Model == "" {
		deps.Model = cfg.Models.Write
	}
	return deps, db, nil
}

// RunSearchSet executes the committed queries against a workspace and
// returns the comparable result rows.
func RunSearchSet(wsDir string, queries []SearchQuery) ([][]Expect, error) {
	return RunSearchSetOpts(wsDir, queries)
}

// RunSearchSetOpts is RunSearchSet with extra vectors.Store options
// (SPEC-06 golden mmap run).
func RunSearchSetOpts(wsDir string, queries []SearchQuery, vecOpts ...vectors.Option) ([][]Expect, error) {
	deps, db, err := searchDeps(wsDir, vecOpts...)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	out := make([][]Expect, 0, len(queries))
	for _, q := range queries {
		req := search.Request{Query: q.Q, Limit: 10, Granularity: search.Docs}
		if len(q.Channels) > 0 {
			parsed, unknown := search.ParseChannels(q.Channels)
			if len(unknown) > 0 {
				return nil, fmt.Errorf("query %q: unknown channels %v", q.Q, unknown)
			}
			req.Channels = parsed
		}
		resp, err := search.Run(context.Background(), deps, req)
		if err != nil {
			return nil, fmt.Errorf("query %q: %w", q.Q, err)
		}
		rows := make([]Expect, 0, len(resp.Results))
		for _, r := range resp.Results {
			doc := r.ArticlePath
			if doc == "" {
				doc = r.DocID
			}
			rows = append(rows, Expect{Doc: doc, Rank: r.Rank, Score: r.FinalScore})
		}
		out = append(out, rows)
	}
	return out, nil
}

// CheckSearchParity compares the search set against the golden.
func CheckSearchParity(wsDir, goldenPath string) error {
	return CheckSearchParityOpts(wsDir, goldenPath)
}

// CheckSearchParityOpts is CheckSearchParity with extra vectors.Store
// options threaded into the search deps (SPEC-06: golden mmap run).
func CheckSearchParityOpts(wsDir, goldenPath string, vecOpts ...vectors.Option) error {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return fmt.Errorf("read search golden: %w", err)
	}
	var golden SearchGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		return fmt.Errorf("parse search golden: %w", err)
	}
	if golden.GoldenFormatVersion != 1 {
		return fmt.Errorf("search golden_format_version %d, want 1", golden.GoldenFormatVersion)
	}

	// Self-determinism: two runs must agree exactly.
	got1, err := RunSearchSetOpts(wsDir, golden.Queries, vecOpts...)
	if err != nil {
		return err
	}
	got2, err := RunSearchSetOpts(wsDir, golden.Queries, vecOpts...)
	if err != nil {
		return err
	}
	if mustJSON(got1) == nil || string(mustJSON(got1)) != string(mustJSON(got2)) {
		return fmt.Errorf("search results not self-deterministic")
	}

	var problems []string
	for i, q := range golden.Queries {
		want := mustJSON(q.Expect)
		got := mustJSON(got1[i])
		if string(want) != string(got) {
			problems = append(problems, fmt.Sprintf("query %q:\n    want %s\n    got  %s", q.Q, want, got))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("search parity failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// CheckRoundTrip exports the workspace, untars it fresh, and re-runs the
// search set for identical answers (SPEC-09 §Assertions 4).
func CheckRoundTrip(wsDir, goldenPath string) error {
	w, err := engine.Open(context.Background(), wsDir, engine.WithReadOnly())
	if err != nil {
		return fmt.Errorf("open for export: %w", err)
	}
	var buf bytes.Buffer
	if err := w.Export(context.Background(), &buf); err != nil {
		w.Close()
		return fmt.Errorf("export: %w", err)
	}
	w.Close()

	fresh := filepath.Join(os.TempDir(), "parity-roundtrip-"+nextSuffix())
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(fresh)
	if err := untar(&buf, fresh); err != nil {
		return fmt.Errorf("untar: %w", err)
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		return err
	}
	var golden SearchGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		return err
	}
	orig, err := RunSearchSet(wsDir, golden.Queries)
	if err != nil {
		return err
	}
	rt, err := RunSearchSet(fresh, golden.Queries)
	if err != nil {
		return fmt.Errorf("round-trip search: %w", err)
	}
	if string(mustJSON(orig)) != string(mustJSON(rt)) {
		return fmt.Errorf("round-trip answers differ:\n  orig %s\n  rt   %s", mustJSON(orig), mustJSON(rt))
	}
	return nil
}

// CheckRoundTripCorruption places one corrupted byte inside a wiki/ file
// in the export and proves the byte check catches it on the re-opened
// workspace (SPEC-09 AC4).
func CheckRoundTripCorruption(wsDir, goldenConfigPath string) error {
	w, err := engine.Open(context.Background(), wsDir, engine.WithReadOnly())
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := w.Export(context.Background(), &buf); err != nil {
		w.Close()
		return err
	}
	w.Close()

	data := buf.Bytes()
	// Corrupt inside a wiki/ payload: walk the tar structure (tar.Reader
	// offsets, correct with PAX/GNU extension blocks) to the first entry
	// whose NAME contains summaries/, then flip the payload midpoint.
	flip := -1
	{
		off := 0
		tr := tar.NewReader(bytes.NewReader(data))
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("walk tar: %w", err)
			}
			hdrEnd := off + 512
			if strings.Contains(hdr.Name, "summaries/") && hdr.Size > 0 {
				flip = hdrEnd + int(hdr.Size)/2
				break
			}
			off = hdrEnd + (int(hdr.Size)+511)/512*512
		}
	}
	if flip < 0 || flip >= len(data) {
		return fmt.Errorf("no summaries payload found in export")
	}
	data[flip] ^= 0xFF

	fresh := filepath.Join(os.TempDir(), "parity-corrupt-"+nextSuffix())
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(fresh)
	if err := untar(bytes.NewReader(data), fresh); err != nil {
		// A tar-level failure is also a passing integrity signal only if
		// the checksum region was hit — our flip is payload, so untar
		// should succeed and byte parity must catch it instead.
		return fmt.Errorf("corrupted payload must still untar (header-only checksums): %w", err)
	}
	err = CheckByteParity(fresh, goldenConfigPath, filepath.Join(filepath.Dir(goldenConfigPath), "byte-parity.json"))
	if err == nil {
		return fmt.Errorf("corrupted byte inside a wiki/ file was NOT detected")
	}
	if !strings.Contains(err.Error(), "summaries/") {
		return fmt.Errorf("byte-parity failure must name the corrupted file, got: %v", err)
	}
	return nil
}

var suffixCounter int

func nextSuffix() string {
	suffixCounter++
	return fmt.Sprintf("%d-%d", os.Getpid(), suffixCounter)
}

func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(hdr.Name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
}
