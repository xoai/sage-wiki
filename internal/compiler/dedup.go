package compiler

import (
	"sync"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
)

const (
	// maxDedupCacheSize caps the cache to prevent unbounded memory growth.
	// At 384-dim embeddings, 50K entries ≈ 75MB. Beyond this, dedup
	// effectiveness plateaus — the most common duplicates are caught early.
	maxDedupCacheSize = 50000
)

// DedupCache uses embedding cosine similarity to detect duplicate concepts.
// Before creating a new concept, embed its name and check against existing
// concept embeddings. If similarity > threshold, merge as alias.
//
// DedupCache is safe for concurrent use.
type DedupCache struct {
	embedder  embed.Embedder
	vecStore  store.VectorStore // optional — load pre-stored embeddings
	threshold float64           // cosine similarity threshold (default 0.85)

	mu    sync.RWMutex // protects cache
	cache map[string][]float32
}

// NewDedupCache creates a dedup cache with the given embedder and threshold.
// If threshold <= 0, defaults to 0.85. vecStore is optional — if provided,
// Seed will load existing embeddings from the store instead of re-embedding.
func NewDedupCache(embedder embed.Embedder, vecStore store.VectorStore, threshold float64) *DedupCache {
	if threshold <= 0 {
		threshold = 0.85
	}
	return &DedupCache{
		embedder:  embedder,
		vecStore:  vecStore,
		threshold: threshold,
		cache:     make(map[string][]float32),
	}
}

// Seed populates the cache with existing concept names.
// Tries to load embeddings from the vector store first (O(1) per concept,
// no API calls). Falls back to embedding via API for concepts not in store.
// Caps at maxDedupCacheSize to prevent unbounded memory growth.
func (dc *DedupCache) Seed(names []string) {
	if dc.embedder == nil {
		return
	}

	if len(names) > maxDedupCacheSize {
		log.Warn("dedup cache: capping seed to max size",
			"requested", len(names), "max", maxDedupCacheSize)
		names = names[:maxDedupCacheSize]
	}

	loaded, embedded, failed := 0, 0, 0
	cacheReadFailures := 0
	cacheWarned := false

	dc.mu.Lock()
	for _, name := range names {
		// Try vector store first (no API call needed)
		if dc.vecStore != nil {
			vec, err := dc.vecStore.Get(name)
			if err == nil && vec != nil {
				dc.cache[name] = vec
				loaded++
				continue
			}
			if err != nil {
				// REL-04: a real DB error is not a cache miss. Count it and
				// fall through to embed (embedding still produces the correct
				// vector) — degrade, never silently swallow. Log the FIRST
				// failure immediately (a 10K-name Seed with a dead store gets
				// feedback now, not at the end) and a summary at the end —
				// never per name: a broken store fails EVERY Get.
				cacheReadFailures++
				if !cacheWarned {
					cacheWarned = true
					log.Warn("vector cache read failed — embedding via API instead (cache retried per concept)",
						"error", err)
				}
			}
		}

		// Fall back to embedding API
		vec, err := dc.embedder.Embed(name)
		if err != nil {
			failed++
			continue
		}
		dc.cache[name] = vec
		embedded++
	}
	cacheSize := len(dc.cache)
	dc.mu.Unlock()

	if cacheSize > 0 {
		log.Info("dedup cache seeded",
			"total", cacheSize, "from_store", loaded,
			"embedded", embedded, "failed", failed)
	}
	if failed > 0 {
		log.Warn("dedup cache: some concepts could not be seeded", "failed", failed)
	}
	if cacheReadFailures > 1 {
		log.Warn("vector cache unreadable during seed — concepts embedded via API instead",
			"cache_read_failures", cacheReadFailures)
	}
}

// CheckDuplicate checks if a concept name is a duplicate of an existing concept.
// Returns the existing concept name, similarity score, and the embedding vector.
// The embedding is returned so callers can pass it to AddWithVec to avoid
// double-embedding.
func (dc *DedupCache) CheckDuplicate(name string) (match string, score float64, vec []float32) {
	dc.mu.RLock()
	cacheEmpty := len(dc.cache) == 0
	dc.mu.RUnlock()

	if dc.embedder == nil || cacheEmpty {
		return "", 0, nil
	}

	var err error
	vec, err = dc.embedder.Embed(name)
	if err != nil {
		return "", 0, nil
	}

	bestMatch := ""
	bestScore := 0.0

	dc.mu.RLock()
	for existing, existingVec := range dc.cache {
		if existing == name {
			continue
		}
		// Guard against dimension mismatch (e.g., provider changed)
		if len(vec) != len(existingVec) {
			continue
		}
		s := vectors.CosineSimilarity(vec, existingVec)
		// SPEC-04 D1: on exact score ties the canonically-first name wins —
		// the merge target must never depend on Go map iteration order.
		if s > bestScore || (s == bestScore && bestMatch != "" && existing < bestMatch) {
			bestScore = s
			bestMatch = existing
		}
	}
	dc.mu.RUnlock()

	if bestScore >= dc.threshold {
		return bestMatch, bestScore, vec
	}

	return "", 0, vec
}

// AddWithVec registers a new concept with a pre-computed embedding.
// Use the vec returned from CheckDuplicate to avoid double-embedding.
func (dc *DedupCache) AddWithVec(name string, vec []float32) {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if len(dc.cache) >= maxDedupCacheSize {
		return // at capacity
	}
	if _, exists := dc.cache[name]; exists {
		return
	}
	dc.cache[name] = vec
}

// Add registers a new concept in the cache by embedding its name.
// Prefer AddWithVec when the embedding is already available.
func (dc *DedupCache) Add(name string) {
	if dc.embedder == nil {
		return
	}
	dc.mu.RLock()
	atCap := len(dc.cache) >= maxDedupCacheSize
	_, exists := dc.cache[name]
	dc.mu.RUnlock()
	if atCap || exists {
		return
	}
	vec, err := dc.embedder.Embed(name)
	if err != nil {
		return
	}
	dc.mu.Lock()
	// Re-check under write lock
	if len(dc.cache) < maxDedupCacheSize {
		if _, exists := dc.cache[name]; !exists {
			dc.cache[name] = vec
		}
	}
	dc.mu.Unlock()
}

// Size returns the number of concepts in the cache.
func (dc *DedupCache) Size() int {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return len(dc.cache)
}
