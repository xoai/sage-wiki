package vectors

import (
	"math"
	"sync"
)

// vectorCache is a per-Store in-memory matrix cache (PERF-01, P1-5): all
// rows of one vector table, L2-normalized at load, so a search is a single
// dot-product pass over a contiguous slice with NO SQL, NO BLOB decode, and
// NO per-vector norm recomputation. Guarded by RWMutex: searches read, the
// initial load and patches write.
//
// Invalidation has two mechanisms by design (see spec §A):
//  1. Incremental patch for Store-owned-WriteTx methods (Upsert/Delete/
//     DeleteDocChunkVectors) — cheap, no reload, hot write loops.
//  2. InvalidateChunkCache (mark-dirty) for caller-owned-tx writes the
//     Store can't observe committing; the NEXT search after a dirty period
//     reloads once, coalescing write bursts.
//
// Patch-on-unloaded rule (load-bearing): a patch arriving while !loaded is
// a NO-OP and must NEVER set loaded — the next search loads the complete
// matrix from the DB. A patch that materialized a partial matrix and
// marked it loaded would serve a permanently incomplete cache.
type vectorCache struct {
	mu     sync.RWMutex
	loaded bool
	dim    int
	ids    []string
	docIDs []string // chunk cache only (row-aligned); nil for the doc cache
	mat    []float32
	loads  int // load counter — test observability

	// ann, when non-nil, is the search structure (P2-7 opt-in). The matrix
	// stays complete as the patch-maintained state; the graph mirrors it.
	ann *hnswBackend
}

func (c *vectorCache) isLoaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

func (c *vectorCache) loadCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loads
}

// dimVal returns the cache's row dimension under the read lock — the
// loaders write dim under the write lock, so a bare read races with a
// concurrent invalidate→reload (Gate-3 i1 MAJOR).
func (c *vectorCache) dimVal() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dim
}

// invalidate marks the cache dirty; the next search reloads from the DB.
func (c *vectorCache) invalidate() {
	c.mu.Lock()
	c.loaded = false
	c.mu.Unlock()
}

// normalizeCopy L2-normalizes v into a COPY (callers own their query
// slice). A zero-norm input yields zeros — dot products with it are 0,
// exactly matching CosineSimilarity's denom==0 → 0 (store.go:293); no NaN.
func normalizeCopy(v []float32) []float32 {
	out := make([]float32, len(v))
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return out // zeros
	}
	inv := 1.0 / float32(math.Sqrt(norm))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// upsert appends or replaces a row (normalized at write). No-op when
// !loaded (patch-on-unloaded rule). A vector whose dimension differs from
// the cached rows (provider/model change mid-process — reembed.go:68 is
// the real trigger) INVALIDATES instead of patching: patching a
// longer/shorter row would silently corrupt the matrix or break every
// row's alignment (Gate-8 MAJOR). The next search reloads coherently.
func (c *vectorCache) upsert(id, docID string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		return
	}
	if len(vec) != c.dim {
		c.loaded = false
		return
	}
	nv := normalizeCopy(vec)
	for i, existing := range c.ids {
		if existing == id {
			copy(c.mat[i*c.dim:(i+1)*c.dim], nv)
			if c.docIDs != nil {
				c.docIDs[i] = docID
			}
			if c.ann != nil {
				c.ann.add(id, docID, nv)
			}
			return
		}
	}
	c.ids = append(c.ids, id)
	if c.docIDs != nil {
		c.docIDs = append(c.docIDs, docID)
	}
	c.mat = append(c.mat, nv...)
	if c.ann != nil {
		c.ann.add(id, docID, nv)
	}
}

// remove swap-removes a row by id. No-op when !loaded.
func (c *vectorCache) remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		return
	}
	for i, existing := range c.ids {
		if existing == id {
			last := len(c.ids) - 1
			c.ids[i] = c.ids[last]
			c.ids = c.ids[:last]
			if c.docIDs != nil {
				c.docIDs[i] = c.docIDs[last]
				c.docIDs = c.docIDs[:last]
			}
			copy(c.mat[i*c.dim:last*c.dim], c.mat[last*c.dim:])
			c.mat = c.mat[:last*c.dim]
			if c.ann != nil {
				c.ann.remove(id)
			}
			return
		}
	}
}

// removeDoc removes all rows belonging to a docID (chunk cache only).
// No-op when !loaded or for the doc cache.
func (c *vectorCache) removeDoc(docID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded || c.docIDs == nil {
		return
	}
	kept := 0
	for i := range c.ids {
		if c.docIDs[i] == docID {
			continue
		}
		if kept != i {
			c.ids[kept] = c.ids[i]
			c.docIDs[kept] = c.docIDs[i]
			copy(c.mat[kept*c.dim:(kept+1)*c.dim], c.mat[i*c.dim:(i+1)*c.dim])
		}
		kept++
	}
	c.ids = c.ids[:kept]
	c.docIDs = c.docIDs[:kept]
	c.mat = c.mat[:kept*c.dim]
	if c.ann != nil {
		c.ann.rebuild(c.ids, c.docIDs, c.mat, c.dim)
	}
}

// cacheResult is one scored row returned from a cache search.
type cacheResult struct {
	id    string
	docID string
	score float64
}

// search scores every row against the normalized query and returns the top
// `limit` results (descending score, insertSorted-equivalent stable order).
// filter (chunk path) restricts rows by docID when non-nil.
//
// The dimension check is REPEATED here under the same RLock that reads the
// matrix: the entry-point guard (dimVal) releases its lock before this
// call, and an invalidate→reload landing between them can change c.dim
// (re-embed with a different provider is a real dim-change path). Without
// the re-check, a longer row dimension would index nq out of range — the
// pre-cache brute-force path was immune via its per-row skip.
func (c *vectorCache) search(nq []float32, limit int, filter map[string]bool) []cacheResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(nq) != c.dim {
		return nil
	}
	if c.ann != nil {
		return c.ann.search(nq, limit, filter)
	}

	top := make([]cacheResult, 0, limit)
	for i := 0; i < len(c.ids); i++ {
		if filter != nil && !filter[c.docIDs[i]] {
			continue
		}
		row := c.mat[i*c.dim : (i+1)*c.dim]
		var dot float64
		for j := range row {
			dot += float64(nq[j]) * float64(row[j])
		}
		res := cacheResult{id: c.ids[i], score: dot}
		if c.docIDs != nil {
			res.docID = c.docIDs[i]
		}
		pos := len(top)
		for pos > 0 && top[pos-1].score < dot {
			pos--
		}
		if pos >= limit {
			continue
		}
		if len(top) < limit {
			top = append(top, cacheResult{})
		}
		copy(top[pos+1:], top[pos:])
		top[pos] = res
		if len(top) > limit {
			top = top[:limit]
		}
	}
	return top
}
