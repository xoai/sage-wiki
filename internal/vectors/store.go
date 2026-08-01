package vectors

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/store"
)

// Vector backend kinds (WithVectorBackend).
const (
	backendMemory = "memory" // default: in-memory matrix cache (P1-5)
	backendMmap   = "mmap"   // on-disk snapshot, mmap-served (SPEC-06)
)

// Store manages vector embeddings as BLOBs in SQLite.
type Store struct {
	db         store.DBHandle
	docCache   *vectorCache
	chunkCache *vectorCache
	ann        bool // P2-7: opt-in HNSW index backend (T6); brute-force default

	// SPEC-06 mmap snapshot state (backendMmap only).
	vecBackend string
	indexDir   string
	mmMu       sync.Mutex
	mmDoc      *mmapTable
	mmChunk    *mmapTable
	// warn-once flags for the fallback/stale transitions.
	mmWarnedFallback bool
	mmWarnedStale    bool
	// mmServed counts searches served by the mmap snapshot — the
	// anti-vacuity signal: parity/recall gates assert it is non-zero so a
	// silent fallback can never pass them.
	mmServed int
}

// Option configures a Store (P2-7).
type Option func(*Store)

// WithANN selects the HNSW approximate index backend (P2-7, opt-in).
// Default (unset) is the exact brute-force cache from P1-5.
func WithANN(enabled bool) Option {
	return func(s *Store) { s.ann = enabled }
}

// WithVectorBackend selects the query-time vector backend: "memory"
// (default, full in-memory matrix cache) or "mmap" (on-disk snapshot
// served via mmap; falls back to memory with a warning when the snapshot
// is missing, corrupt, or stale). "mmap" requires WithIndexDir.
func WithVectorBackend(kind string) Option {
	return func(s *Store) { s.vecBackend = kind }
}

// WithIndexDir sets the directory holding vectors.idx / vectors-chunks.idx
// (the workspace .sage dir).
func WithIndexDir(dir string) Option {
	return func(s *Store) { s.indexDir = dir }
}

// IndexKind reports the configured index backend ("brute-force" or
// "hnsw") — makes the WithANN plumbing observable (T6 implements the
// HNSW backend behind it).
func (s *Store) IndexKind() string {
	if s.ann {
		return "hnsw"
	}
	return "brute-force"
}

// VectorBackend reports the configured query-time backend ("memory" or
// "mmap") — makes the WithVectorBackend plumbing observable.
func (s *Store) VectorBackend() string {
	if s.vecBackend == "" {
		return backendMemory
	}
	return s.vecBackend
}

// MmapServedCount reports how many searches the mmap snapshot has served
// (anti-vacuity observability for the SPEC-06 gates).
func (s *Store) MmapServedCount() int {
	s.mmMu.Lock()
	defer s.mmMu.Unlock()
	return s.mmServed
}

// loadDocCache populates the doc-level cache from SQLite, single-flight:
// the first caller takes the write lock and RE-CHECKS loaded
// (double-checked locking), so concurrent first searches load exactly once.
func (s *Store) loadDocCache() error {
	s.docCache.mu.RLock()
	loaded := s.docCache.loaded
	s.docCache.mu.RUnlock()
	if loaded {
		metrics.CounterNamed("vector_cache_hits_total", "cache", "doc").Inc() // served-from-loaded, race-free inside the loader (P2-2)
		return nil
	}

	s.docCache.mu.Lock()
	defer s.docCache.mu.Unlock()
	if s.docCache.loaded {
		metrics.CounterNamed("vector_cache_hits_total", "cache", "doc").Inc() // blocked-then-loaded still serves from the loaded matrix (P2-2)
		return nil
	}
	metrics.CounterNamed("vector_cache_misses_total", "cache", "doc").Inc() // actual reload (P2-2)

	rows, err := s.db.ReadDB().Query("SELECT id, embedding, dimensions FROM vec_entries ORDER BY rowid")
	if err != nil {
		return fmt.Errorf("vectors.loadDocCache: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []string{}
	mat := []float32{}
	dim := 0
	skipped := 0
	for rows.Next() {
		var id string
		var blob []byte
		var dims int
		if err := rows.Scan(&id, &blob, &dims); err != nil {
			return err
		}
		vec := decodeFloat32s(blob)
		if dim == 0 {
			dim = len(vec)
		}
		if len(vec) != dim {
			skipped++ // dimension-mismatch rows skipped (as in Search)
			continue
		}
		ids = append(ids, id)
		mat = append(mat, normalizeCopy(vec)...)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if skipped > 0 {
		log.Warn("vector cache: skipped dimension-mismatch rows", "count", skipped)
	}

	s.docCache.dim = dim
	s.docCache.ids = ids
	s.docCache.mat = mat
	if s.ann {
		if s.docCache.ann == nil {
			s.docCache.ann = newHNSWBackend()
		}
		s.docCache.ann.rebuild(ids, nil, mat, dim)
	}
	s.docCache.loads++
	s.docCache.loaded = true
	return nil
}

// loadChunkCache populates the chunk-level cache (same single-flight rule).
func (s *Store) loadChunkCache() error {
	s.chunkCache.mu.RLock()
	loaded := s.chunkCache.loaded
	s.chunkCache.mu.RUnlock()
	if loaded {
		metrics.CounterNamed("vector_cache_hits_total", "cache", "chunk").Inc() // served-from-loaded (P2-2)
		return nil
	}

	s.chunkCache.mu.Lock()
	defer s.chunkCache.mu.Unlock()
	if s.chunkCache.loaded {
		metrics.CounterNamed("vector_cache_hits_total", "cache", "chunk").Inc() // blocked-then-loaded (P2-2)
		return nil
	}
	metrics.CounterNamed("vector_cache_misses_total", "cache", "chunk").Inc() // actual reload (P2-2)

	rows, err := s.db.ReadDB().Query("SELECT chunk_id, doc_id, embedding, dimensions FROM vec_chunks ORDER BY rowid")
	if err != nil {
		return fmt.Errorf("vectors.loadChunkCache: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := []string{}
	docIDs := []string{}
	mat := []float32{}
	dim := 0
	skipped := 0
	for rows.Next() {
		var cid, did string
		var blob []byte
		var dims int
		if err := rows.Scan(&cid, &did, &blob, &dims); err != nil {
			return err
		}
		vec := decodeFloat32s(blob)
		if dim == 0 {
			dim = len(vec)
		}
		if len(vec) != dim {
			skipped++
			continue
		}
		ids = append(ids, cid)
		docIDs = append(docIDs, did)
		mat = append(mat, normalizeCopy(vec)...)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if skipped > 0 {
		log.Warn("chunk vector cache: skipped dimension-mismatch rows", "count", skipped)
	}

	s.chunkCache.dim = dim
	s.chunkCache.ids = ids
	s.chunkCache.docIDs = docIDs
	s.chunkCache.mat = mat
	if s.ann {
		if s.chunkCache.ann == nil {
			s.chunkCache.ann = newHNSWBackend()
		}
		s.chunkCache.ann.rebuild(ids, docIDs, mat, dim)
	}
	s.chunkCache.loads++
	s.chunkCache.loaded = true
	return nil
}

// InvalidateChunkCache marks the chunk-level cache dirty; the next chunk
// search reloads from the DB. Callers that write vec_chunks inside their
// OWN transaction (UpsertChunk with a caller tx, ChunkStore.DeleteDocChunks)
// MUST call this after their WriteTx commits — the Store cannot observe
// the commit itself (spec §A, mechanism 2). Coalesces write bursts into
// one reload at the next search.
func (s *Store) InvalidateChunkCache() {
	s.chunkCache.invalidate()
	s.markStale(true)
}

// Upsert stores or replaces a vector.
func (s *Store) Upsert(id string, embedding []float32) error {
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		blob := encodeFloat32s(embedding)
		_, err := tx.Exec(
			`INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
			id, blob, len(embedding),
		)
		return err
	})
	if err != nil {
		return err
	}
	s.docCache.upsert(id, "", embedding)
	s.markStale(false)
	return nil
}

// NewStore creates a new vector store.
func NewStore(db store.DBHandle, opts ...Option) *Store {
	s := &Store{
		db:         db,
		docCache:   &vectorCache{},
		chunkCache: &vectorCache{docIDs: []string{}},
	}
	for _, opt := range opts {
		opt(s)
	}
	// ann+mmap precedence (SPEC-06): the mmap snapshot serves an exact scan;
	// the HNSW graph builds only inside the in-memory loaders the snapshot
	// path bypasses — keeping ann enabled would silently waste the graph
	// build on every fallback load. Exact scan wins, loudly.
	if s.ann && s.vecBackend == backendMmap {
		log.Warn("search.ann.enabled is ignored with vectors.backend=mmap — the mmap snapshot serves an exact scan")
		s.ann = false
	}
	return s
}

// Get retrieves a vector by ID. Returns (nil, nil) ONLY when the ID is
// genuinely absent (sql.ErrNoRows); any other error (closed/corrupt DB) is
// wrapped and returned — a real failure must never masquerade as a cache
// miss (REL-04).
func (s *Store) Get(id string) ([]float32, error) {
	var blob []byte
	err := s.db.ReadDB().QueryRow("SELECT embedding FROM vec_entries WHERE id=?", id).Scan(&blob)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("vectors.Get: %w", err)
	}
	// A blob that is empty or not a multiple of 4 bytes cannot be a valid
	// embedding — decodeFloat32s would silently return a non-nil empty or
	// garbage vector that callers read as a cache HIT (dedup Seed poisons its
	// cache with it). Corrupt data must not masquerade as a hit (REL-04).
	if len(blob) == 0 || len(blob)%4 != 0 {
		return nil, fmt.Errorf("vectors.Get: corrupt embedding blob for %q (%d bytes)", id, len(blob))
	}
	return decodeFloat32s(blob), nil
}

// Delete removes a vector by ID.
func (s *Store) Delete(id string) error {
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_entries WHERE id=?", id)
		return err
	})
	if err != nil {
		return err
	}
	s.docCache.remove(id)
	s.markStale(false)
	return nil
}

// VectorResult represents a cosine similarity search result.
type VectorResult = store.VectorResult

// Search performs cosine similarity search over the in-memory matrix cache
// (PERF-01): one lazy load from SQLite, then dot-product passes with no
// SQL, no BLOB decode, and no per-vector norm recomputation. The query is
// normalized into a COPY — the caller's slice is never mutated.
func (s *Store) Search(query []float32, limit int) ([]VectorResult, error) {
	if limit <= 0 {
		limit = 10
	}
	if s.vecBackend == backendMmap {
		if idx := s.mmapFor(false); idx != nil {
			nq := normalizeCopy(query)
			hits := searchMmap(idx, nq, limit, nil)
			results := make([]VectorResult, len(hits))
			for i, h := range hits {
				results[i] = VectorResult{ID: h.id, Score: h.score, Rank: i + 1}
			}
			return results, nil
		}
	}
	if err := s.loadDocCache(); err != nil {
		return nil, err
	}
	// Dimension-mismatch guard (preserved from the brute-force path): a
	// query whose length differs from the cache's row dimension matches
	// nothing — including a nil/empty query from a caller with no embedder.
	if len(query) != s.docCache.dimVal() {
		return nil, nil
	}

	nq := normalizeCopy(query)
	hits := s.docCache.search(nq, limit, nil)
	results := make([]VectorResult, len(hits))
	for i, h := range hits {
		results[i] = VectorResult{ID: h.id, Score: h.score, Rank: i + 1}
	}
	return results, nil
}

// UpsertChunk stores or replaces a chunk vector within an existing transaction.
func (s *Store) UpsertChunk(tx *sql.Tx, chunkID string, docID string, embedding []float32) error {
	blob := encodeFloat32s(embedding)
	_, err := tx.Exec(
		`INSERT INTO vec_chunks (chunk_id, doc_id, embedding, dimensions) VALUES (?, ?, ?, ?)
		 ON CONFLICT(chunk_id) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
		chunkID, docID, blob, len(embedding),
	)
	return err
}

// SearchChunks performs cosine similarity search on chunk vectors, backed
// by the chunk-level in-memory cache (PERF-01).
func (s *Store) SearchChunks(query []float32, limit int) ([]ChunkVectorResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if s.vecBackend == backendMmap {
		if idx := s.mmapFor(true); idx != nil {
			nq := normalizeCopy(query)
			hits := searchMmap(idx, nq, limit, nil)
			results := make([]ChunkVectorResult, len(hits))
			for i, h := range hits {
				results[i] = ChunkVectorResult{ChunkID: h.id, DocID: h.docID, Score: h.score, Rank: i + 1}
			}
			return results, nil
		}
	}
	if err := s.loadChunkCache(); err != nil {
		return nil, err
	}
	if len(query) != s.chunkCache.dimVal() {
		return nil, nil
	}

	nq := normalizeCopy(query)
	hits := s.chunkCache.search(nq, limit, nil)
	results := make([]ChunkVectorResult, len(hits))
	for i, h := range hits {
		results[i] = ChunkVectorResult{ChunkID: h.id, DocID: h.docID, Score: h.score, Rank: i + 1}
	}
	return results, nil
}

// SearchChunksFiltered performs cosine search only on chunks belonging to the given doc IDs.
// This is the BM25-prefiltered path that caps vector comparisons.
func (s *Store) SearchChunksFiltered(query []float32, docIDs []string, limit int) ([]ChunkVectorResult, error) {
	if limit <= 0 {
		limit = 20
	}
	if len(docIDs) == 0 {
		return nil, nil
	}

	// Cap doc IDs to 100
	if len(docIDs) > 100 {
		docIDs = docIDs[:100]
	}

	filter := make(map[string]bool, len(docIDs))
	for _, id := range docIDs {
		filter[id] = true
	}
	nq := normalizeCopy(query)
	if s.vecBackend == backendMmap {
		if idx := s.mmapFor(true); idx != nil {
			hits := searchMmap(idx, nq, limit, filter)
			results := make([]ChunkVectorResult, len(hits))
			for i, h := range hits {
				results[i] = ChunkVectorResult{ChunkID: h.id, DocID: h.docID, Score: h.score, Rank: i + 1}
			}
			return results, nil
		}
	}
	if err := s.loadChunkCache(); err != nil {
		return nil, err
	}
	if len(query) != s.chunkCache.dimVal() {
		return nil, nil
	}
	hits := s.chunkCache.search(nq, limit, filter)
	results := make([]ChunkVectorResult, len(hits))
	for i, h := range hits {
		results[i] = ChunkVectorResult{ChunkID: h.id, DocID: h.docID, Score: h.score, Rank: i + 1}
	}
	return results, nil
}

// DeleteDocChunkVectors removes all chunk vectors for a document.
func (s *Store) DeleteDocChunkVectors(docID string) error {
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM vec_chunks WHERE doc_id = ?", docID)
		return err
	})
	if err != nil {
		return err
	}
	s.chunkCache.removeDoc(docID)
	s.markStale(true)
	return nil
}

// HasChunkVectors reports whether any chunk vector exists for docID. The
// reconciler uses it to detect an output that is indexed in FTS but whose vector
// embedding was deferred (offline) or lost, so it can be filled in once an
// embedder is available.
func (s *Store) HasChunkVectors(docID string) (bool, error) {
	var one int
	err := s.db.ReadDB().QueryRow("SELECT 1 FROM vec_chunks WHERE doc_id = ? LIMIT 1", docID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vectors.HasChunkVectors: %w", err)
	}
	return true, nil
}

// ChunkVectorResult represents a chunk cosine similarity search result.
type ChunkVectorResult = store.ChunkVectorResult

// insertChunkSorted maintains a sorted slice of top-k chunk results (descending by score).
func insertChunkSorted(results []ChunkVectorResult, item ChunkVectorResult, limit int) []ChunkVectorResult {
	pos := len(results)
	for pos > 0 && results[pos-1].Score < item.Score {
		pos--
	}
	if pos >= limit {
		return results
	}
	if len(results) < limit {
		results = append(results, ChunkVectorResult{})
	}
	copy(results[pos+1:], results[pos:])
	results[pos] = item
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// Count returns the total number of stored vectors.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM vec_entries").Scan(&count)
	return count, err
}

// Dimensions returns the dimension count of the first stored vector, or 0 if empty.
func (s *Store) Dimensions() (int, error) {
	var dims int
	err := s.db.ReadDB().QueryRow("SELECT COALESCE(MAX(dimensions), 0) FROM vec_entries").Scan(&dims)
	return dims, err
}

// CosineSimilarity computes cosine similarity between two vectors.
// Returns 0 if vectors have different dimensions (safe for mixed-provider scenarios).
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// encodeFloat32s converts []float32 to []byte (little-endian).
func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeFloat32s converts []byte (little-endian) to []float32.
func decodeFloat32s(buf []byte) []float32 {
	v := make([]float32, len(buf)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}

// insertSorted maintains a sorted slice of top-k results (descending by score).
func insertSorted(results []VectorResult, item VectorResult, limit int) []VectorResult {
	pos := len(results)
	for pos > 0 && results[pos-1].Score < item.Score {
		pos--
	}

	if pos >= limit {
		return results
	}

	if len(results) < limit {
		results = append(results, VectorResult{})
	}

	copy(results[pos+1:], results[pos:])
	results[pos] = item

	if len(results) > limit {
		results = results[:limit]
	}

	return results
}
