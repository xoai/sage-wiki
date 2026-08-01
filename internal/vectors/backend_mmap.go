package vectors

import (
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
)

// mmapTable is one table's read-only snapshot state (SPEC-06). The
// snapshot NEVER receives writes: any write-side invalidation marks it
// stale, and search falls back to the in-memory path until the next
// `index rebuild-vectors`. Correctness never depends on freshness.
type mmapTable struct {
	idx   *parsedIndex
	unmap func() error
	stale bool
	tried bool // load attempted (success or failure) — no per-search re-stat
}

// indexFilenames: one file per vec table inside the workspace .sage dir.
const (
	docIndexFile   = "vectors.idx"
	chunkIndexFile = "vectors-chunks.idx"
)

// mmapFor returns the usable snapshot for a table, or nil when the
// in-memory path must serve (missing/corrupt/stale index, or fallback
// platform). Every nil transition warns exactly once per Store.
func (s *Store) mmapFor(chunk bool) *parsedIndex {
	s.mmMu.Lock()
	defer s.mmMu.Unlock()

	t := &s.mmDoc
	probe := docTable.probeSQL
	file := docIndexFile
	if chunk {
		t = &s.mmChunk
		probe = chunkTable.probeSQL
		file = chunkIndexFile
	}
	if *t == nil {
		*t = &mmapTable{}
	}
	mt := *t

	if mt.stale {
		s.warnStaleOnce(chunk)
		return nil
	}
	if !mt.tried {
		mt.tried = true
		path := filepath.Join(s.indexDir, file)
		data, unmap, err := mapFile(path)
		if err != nil {
			s.warnFallbackOnce(chunk, "index unavailable: "+err.Error())
			return nil
		}
		idx, err := parseIndex(data)
		if err != nil {
			_ = unmap()
			s.warnFallbackOnce(chunk, err.Error())
			return nil
		}
		// Coherence probes ONCE at load (not per-search: intra-process
		// writes mark the snapshot stale explicitly, and a per-search
		// full-table scan would dominate query cost — P7 benchmark).
		//
		// 1. Row-shape probe: count exactly the rows the writer would
		//    have included (decoded-length predicate — the dimensions
		//    column is advisory and unused by the loader). Catches
		//    count/dim drift.
		var count int
		if err := s.db.ReadDB().QueryRow(probe, idx.header.dim).Scan(&count); err != nil {
			_ = unmap()
			s.warnFallbackOnce(chunk, "coherence probe failed: "+err.Error())
			return nil
		}
		if count != int(idx.header.count) {
			_ = unmap()
			mt.stale = true
			s.warnStaleOnce(chunk)
			return nil
		}
		// 2. Content-drift probe (F-047): any DB write newer than the
		//    index file marks it stale — the count probe is blind to
		//    same-count re-embeds and to empty-index-then-populate
		//    (sequential processes are NOT covered by markStale or the
		//    workspace lock). False positives (a checkpoint or an
		//    unrelated-table write) fall back to memory — the safe
		//    direction. Missing db files (custom layouts, tests) skip
		//    the check; the count probe still applies.
		if idxInfo, statErr := os.Stat(path); statErr == nil {
			for _, dbFile := range []string{"wiki.db", "wiki.db-wal"} {
				fi, err := os.Stat(filepath.Join(s.indexDir, dbFile))
				if err != nil {
					continue
				}
				if fi.ModTime().After(idxInfo.ModTime()) {
					_ = unmap()
					mt.stale = true
					s.warnStaleOnce(chunk)
					return nil
				}
			}
		}
		mt.idx, mt.unmap = idx, unmap
		if !mmapIsReal {
			log.Warn("vectors.backend=mmap: real mmap unavailable on this platform — " +
				"index is fully resident; the bounded-memory ceiling is unix-only this cycle")
		}
	}
	if mt.idx == nil {
		return nil
	}
	s.mmServed++
	return mt.idx
}

// warnFallbackOnce logs the one-time missing/corrupt fallback warning.
func (s *Store) warnFallbackOnce(chunk bool, why string) {
	if s.mmWarnedFallback {
		return
	}
	s.mmWarnedFallback = true
	log.Warn("vector index snapshot unusable — falling back to in-memory cache",
		"table", tableName(chunk), "reason", why,
		"hint", "run: sage-wiki index rebuild-vectors")
}

// warnStaleOnce logs the one-time staleness warning.
func (s *Store) warnStaleOnce(chunk bool) {
	if s.mmWarnedStale {
		return
	}
	s.mmWarnedStale = true
	log.Warn("vector index snapshot is stale — falling back to in-memory cache until rebuild",
		"table", tableName(chunk), "hint", "run: sage-wiki index rebuild-vectors")
}

func tableName(chunk bool) string {
	if chunk {
		return "vec_chunks"
	}
	return "vec_entries"
}

// markStale invalidates the snapshot after a write-side change. The table
// struct is created if the snapshot was never loaded — a write arriving
// before the first mmap search must still force the memory path (the
// count probe alone cannot catch a delete+reinsert of equal size).
func (s *Store) markStale(chunk bool) {
	if s.vecBackend != backendMmap {
		return
	}
	s.mmMu.Lock()
	defer s.mmMu.Unlock()
	if chunk {
		if s.mmChunk == nil {
			s.mmChunk = &mmapTable{}
		}
		s.mmChunk.stale = true
		return
	}
	if s.mmDoc == nil {
		s.mmDoc = &mmapTable{}
	}
	s.mmDoc.stale = true
}

// closeMmap unmaps both snapshots. Idempotent; called from Store.Close.
func (s *Store) closeMmap() {
	s.mmMu.Lock()
	defer s.mmMu.Unlock()
	for _, t := range []*mmapTable{s.mmDoc, s.mmChunk} {
		if t != nil && t.unmap != nil {
			if err := t.unmap(); err != nil {
				log.Warn("vectors: unmap failed", "error", err)
			}
			t.unmap = nil
			t.idx = nil
		}
	}
}

// searchMmap scores every snapshot row against the normalized query and
// returns the top-limit results — the same insert-stable ordering as
// vectorCache.search, over the same normalized rows in the same rowid
// order, so fp32 results are identical to the in-memory path.
func searchMmap(idx *parsedIndex, nq []float32, limit int, filter map[string]bool) []cacheResult {
	dim := idx.header.dim
	if len(nq) != dim || idx.header.count == 0 {
		return nil
	}
	row := make([]float32, dim)
	top := make([]cacheResult, 0, limit)
	for i := 0; i < int(idx.header.count); i++ {
		if filter != nil && !filter[idx.docIDs[i]] {
			continue
		}
		var dot float64
		if row32 := idx.fp32Row(i); row32 != nil {
			// fp32 fast path: the matrix is reinterpreted in place (ids
			// section is 4-byte padded), so this is ONE pass over the
			// mapped pages — same cost shape as the in-memory matrix.
			for j, v := range row32 {
				dot += float64(nq[j]) * float64(v)
			}
		} else {
			idx.rowInto(i, row)
			for j := range row {
				dot += float64(nq[j]) * float64(row[j])
			}
		}
		res := cacheResult{id: idx.ids[i], score: dot}
		if idx.docIDs != nil {
			res.docID = idx.docIDs[i]
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

// Store.Close releases mapped index files. Idempotent. The DB itself is
// owned by the caller (DBHandle) and is NOT closed here.
func (s *Store) Close() error {
	s.closeMmap()
	return nil
}
