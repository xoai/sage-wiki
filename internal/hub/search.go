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

	// Sort by score descending, then apply RRF re-ranking
	sort.Slice(all, func(i, j int) bool {
		return all[i].RRFScore > all[j].RRFScore
	})

	k := 60.0
	for i := range all {
		all[i].RRFScore = 1.0 / (k + float64(i) + 1)
	}

	if len(all) > limit {
		all = all[:limit]
	}

	return all, nil
}

func searchProject(projectDir string, query string, limit int) ([]hybrid.SearchResult, error) {
	db, err := storage.Open(filepath.Join(projectDir, ".sage", "wiki.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	mem := memory.NewStore(db)
	vec := vectors.NewStore(db)
	searcher := hybrid.NewSearcher(mem, vec)

	var queryVec []float32
	var bm25W, vecW float64
	cfg, cfgErr := config.Load(filepath.Join(projectDir, "config.yaml"))
	if cfgErr == nil {
		if embedder := embed.NewFromConfig(cfg); embedder != nil {
			queryVec, _ = embedder.Embed(query)
		}
		bm25W = cfg.Search.HybridWeightBM25
		vecW = cfg.Search.HybridWeightVector
	}

	return searcher.Search(hybrid.SearchOpts{
		Query: query, Limit: limit, BM25Weight: bm25W, VectorWeight: vecW,
	}, queryVec)
}
