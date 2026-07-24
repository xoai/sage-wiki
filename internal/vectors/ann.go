package vectors

import (
	"math"
	"math/rand"

	"github.com/xoai/sage-wiki/internal/vectors/hnsw"
)

// hnswBackend is the ANN search structure for one cache (P2-7). It is a
// DERIVED structure: the matrix in vectorCache stays the complete,
// patch-maintained state, and the graph mirrors it — keeping every P1-5
// cache discipline (single-flight load, patch-on-unloaded no-op,
// invalidation reload) byte-identical. All calls happen under the owning
// cache's lock; the backend has no locking of its own.
//
// Memory note (documented, opt-in): matrix + graph coexist. The win is
// query time on very large vaults; a future optimization can drop the
// matrix once the graph proves out in production.
type hnswBackend struct {
	graph   *hnsw.Graph[string]
	docByID map[string]string // chunk cache: chunkID → docID
}

func newHNSWBackend() *hnswBackend {
	return &hnswBackend{
		graph:   newGraph(),
		docByID: make(map[string]string),
	}
}

// newGraph builds the graph with tuned parameters — shared by the
// constructor and rebuild so an invalidation reload keeps them.
func newGraph() *hnsw.Graph[string] {
	g := hnsw.NewGraph[string]()
	// EfSearch raised from the library default (20): on adversarial
	// high-dimensional data (distance concentration), ef=20 underperforms
	// (recall@10 ≈ 6/10 vs exact, ef=50 ≈ 9.0, ef=100 ≈ 9.8); ef=200 keeps
	// every probe ≥9/10 at still-trivial cost versus a brute scan. Rng is
	// fixed-seed so a given corpus builds the same graph every time
	// (reproducible behavior + stable tests).
	g.EfSearch = 200
	g.Rng = rand.New(rand.NewSource(1))
	return g
}

// add upserts a node (normalized vector, same as the matrix rows).
func (b *hnswBackend) add(id, docID string, nv []float32) {
	b.graph.Delete(id) // replace semantics on re-add
	b.graph.Add(hnsw.MakeNode(id, nv))
	if docID != "" {
		b.docByID[id] = docID
	}
}

func (b *hnswBackend) remove(id string) {
	b.graph.Delete(id)
	delete(b.docByID, id)
}

// rebuild reconstructs the graph from the loaded matrix rows (called by
// the loader when the cache (re)loads — invalidation path included).
func (b *hnswBackend) rebuild(ids []string, docIDs []string, mat []float32, dim int) {
	b.graph = newGraph()
	b.docByID = make(map[string]string, len(ids))
	for i, id := range ids {
		// COPY the row: graph nodes must own their vectors. Aliasing the
		// matrix backing array lets cache.remove()'s swap-truncate + a
		// later append overwrite a surviving node's vector with a
		// different row's bytes (gate-review CRITICAL).
		row := make([]float32, dim)
		copy(row, mat[i*dim:(i+1)*dim])
		var did string
		if docIDs != nil {
			did = docIDs[i]
			b.docByID[id] = did
		}
		b.graph.Add(hnsw.MakeNode(id, row))
	}
}

// search returns the top-limit rows by cosine similarity. Filtered
// searches over-fetch limit*4 and filter post-search — under extreme
// filter selectivity this may return fewer than limit results
// (documented divergence from the exact path, spec R3).
func (b *hnswBackend) search(nq []float32, limit int, filter map[string]bool) []cacheResult {
	if b.graph.Len() == 0 {
		return nil
	}
	fetch := limit
	if filter != nil {
		fetch = limit * 4
		if fetch < 16 {
			fetch = 16
		}
	}
	if fetch > b.graph.Len() {
		fetch = b.graph.Len()
	}
	nodes := b.graph.Search(nq, fetch)
	out := make([]cacheResult, 0, limit)
	for _, n := range nodes {
		d := float64(hnsw.CosineDistance(nq, n.Value))
		if math.IsNaN(d) {
			continue // zero-norm row or query — brute path scores these 0 (skip, not NaN)
		}
		did := b.docByID[n.Key]
		if filter != nil && !filter[did] {
			continue
		}
		out = append(out, cacheResult{
			id:    n.Key,
			docID: did,
			score: 1 - d,
		})
		if len(out) == limit {
			break
		}
	}
	return out
}
