package hub

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// FederatedResult is a search result tagged with source project.
type FederatedResult struct {
	Project     string   `json:"project"`
	ArticlePath string   `json:"article_path"`
	Content     string   `json:"content"`
	RRFScore    float64  `json:"rrf_score"`
	Tags        []string `json:"tags,omitempty"`
}

// FederatedSearch searches multiple projects in parallel with RRF merge.
func FederatedSearch(projects map[string]Project, query string, limit int) ([]FederatedResult, error) {
	type projectResult struct {
		name    string
		results []hybrid.SearchResult
		err     error
	}

	var wg sync.WaitGroup
	ch := make(chan projectResult, len(projects))

	for name, proj := range projects {
		wg.Add(1)
		go func(name string, proj Project) {
			defer wg.Done()
			results, err := searchProject(proj.Path, query, limit)
			ch <- projectResult{name: name, results: results, err: err}
		}(name, proj)
	}

	wg.Wait()
	close(ch)

	var all []FederatedResult
	var errCount int
	for pr := range ch {
		if pr.err != nil {
			fmt.Printf("warning: search %s failed: %v\n", pr.name, pr.err)
			errCount++
			continue
		}
		for _, r := range pr.results {
			all = append(all, FederatedResult{
				Project:     pr.name,
				ArticlePath: r.ArticlePath,
				Content:     r.Content,
				RRFScore:    r.RRFScore,
				Tags:        r.Tags,
			})
		}
	}

	// W2 fix: if ALL projects failed, return error
	if errCount == len(projects) {
		return nil, fmt.Errorf("all %d projects failed to search", errCount)
	}

	// Sort by per-project RRF score descending, then truncate.
	// Do NOT re-score with positional RRF — that would discard magnitude.
	sort.Slice(all, func(i, j int) bool {
		return all[i].RRFScore > all[j].RRFScore
	})

	if len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func searchProject(projectDir string, query string, limit int) ([]hybrid.SearchResult, error) {
	// P2-1 T11: reader mode (no migrations, no advisory lock) with the
	// hub-sized read pool (4/2 — N projects × writer pools would exhaust
	// max_connections on postgres). Config load failure or reader-open
	// failure (e.g. schema behind → needs a writer pass) falls back to the
	// legacy writer open — today's behavior in both cases.
	var b store.Backend
	cfg, cfgErr := config.Load(filepath.Join(projectDir, "config.yaml"))
	if cfgErr == nil {
		lt, _ := cfg.Storage.LockTimeoutDuration()
		b, _ = storedial.Open(cfg.Storage, store.OpenOptions{
			Mode:            store.ModeReader,
			ProjectDir:      projectDir,
			LockTimeout:     lt,
			Pool:            store.PoolConfig{MaxOpen: 4, MaxIdle: 2},
			TemporalEnabled: cfg.Ontology.Temporal.Enabled,
			ANN:             cfg.Search.ANNEnabled(),
		})
	}
	if b == nil {
		b, _ = storedial.Open(config.StorageConfig{}, store.OpenOptions{
			Mode:       store.ModeWriter,
			ProjectDir: projectDir,
		})
	}
	if b == nil {
		// Last resort: legacy direct open (unreachable in practice —
		// sqlite writer open rarely fails when the file exists).
		db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return searchWithStores(hybrid.NewSearcher(memory.NewStore(db), vectors.NewStore(db,
			vectors.WithVectorBackend(cfg.VectorBackend()),
			vectors.WithIndexDir(filepath.Join(projectDir, ".sage")))), cfg, query, limit, trust.NewStore(db))
	}
	defer b.Close()

	return searchWithStores(hybrid.NewSearcher(b.Entries(), b.Vectors()), cfg, query, limit, b.Trust())
}

func searchWithStores(searcher *hybrid.Searcher, cfg *config.Config, query string, limit int, ts trust.ConfirmationChecker) ([]hybrid.SearchResult, error) {
	var queryVec []float32
	var bm25W, vecW float64
	if cfg != nil {
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			queryVec, _ = embedder.Embed(query)
		}
		bm25W = cfg.Search.HybridWeightBM25
		vecW = cfg.Search.HybridWeightVector
	}

	hits, err := searcher.Search(hybrid.SearchOpts{
		Query: query, Limit: limit, BM25Weight: bm25W, VectorWeight: vecW,
	}, queryVec)
	if err != nil {
		return nil, err
	}

	// Hub search is the sixth search surface and still on the legacy doc
	// path, but the trust rule is not the pipeline's — it is the wiki's.
	// Without this, `hub search` returns unverified `output:` answers that
	// every other surface (and Q&A itself) refuses to show. The project's
	// own trust store is passed in, so "verified" means verified here too
	// rather than degrading to "exclude everything".
	mode := "false"
	if cfg != nil {
		mode = cfg.Trust.IncludeOutputsMode()
	}
	include := trust.IncludePredicate(mode, ts)
	kept := hits[:0]
	for _, h := range hits {
		if include(h.ID) {
			kept = append(kept, h)
		}
	}
	return kept, nil
}
