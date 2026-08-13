package compiler

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// mockEmbedder returns predictable embeddings for testing.
type mockEmbedder struct {
	embeddings map[string][]float32
}

func (m *mockEmbedder) Embed(text string) ([]float32, error) {
	if vec, ok := m.embeddings[text]; ok {
		return vec, nil
	}
	// Generate a simple hash-based vector for unknown texts
	vec := make([]float32, 4)
	for i, r := range text {
		vec[i%4] += float32(r) / 1000.0
	}
	// Normalize
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec, nil
}

func (m *mockEmbedder) Name() string    { return "mock" }
func (m *mockEmbedder) Dimensions() int { return 4 }

func TestDedupCache_SimilarConcepts(t *testing.T) {
	// Create embeddings where "flash attention" and "flash-attention" are very similar
	// but "database indexing" is very different
	flashVec := []float32{0.9, 0.1, 0.0, 0.1}
	flashAltVec := []float32{0.88, 0.12, 0.01, 0.09}
	dbVec := []float32{0.1, 0.1, 0.9, 0.1}

	embedder := &mockEmbedder{
		embeddings: map[string][]float32{
			"flash attention":   flashVec,
			"flash-attention":   flashAltVec,
			"database indexing": dbVec,
		},
	}

	dc := NewDedupCache(embedder, nil, 0.85)

	// Seed with existing concept
	dc.Seed([]string{"flash attention"})

	// Check similar concept — should match
	match, score, vec := dc.CheckDuplicate("flash-attention")
	sim := vectors.CosineSimilarity(flashVec, flashAltVec)
	t.Logf("flash attention vs flash-attention: cosine=%.4f, match=%q, score=%.4f", sim, match, score)

	if sim < 0.85 {
		t.Skipf("mock embeddings not similar enough (%.4f), adjusting test", sim)
	}
	if match != "flash attention" {
		t.Errorf("expected match 'flash attention', got %q (score %.4f)", match, score)
	}
	if vec == nil {
		t.Error("expected non-nil vec from CheckDuplicate")
	}

	// Check dissimilar concept — should not match
	match2, score2, _ := dc.CheckDuplicate("database indexing")
	if match2 != "" {
		t.Errorf("expected no match for 'database indexing', got %q (score %.4f)", match2, score2)
	}
}

func TestDedupCache_Add(t *testing.T) {
	embedder := &mockEmbedder{embeddings: map[string][]float32{
		"concept-a": {0.5, 0.5, 0.0, 0.0},
	}}

	dc := NewDedupCache(embedder, nil, 0.85)
	if dc.Size() != 0 {
		t.Errorf("initial size = %d, want 0", dc.Size())
	}

	dc.Add("concept-a")
	if dc.Size() != 1 {
		t.Errorf("after add size = %d, want 1", dc.Size())
	}

	// Adding same concept again should not increase size
	dc.Add("concept-a")
	if dc.Size() != 1 {
		t.Errorf("after duplicate add size = %d, want 1", dc.Size())
	}
}

func TestDedupCache_NilEmbedder(t *testing.T) {
	dc := NewDedupCache(nil, nil, 0.85)

	dc.Seed([]string{"test"})
	if dc.Size() != 0 {
		t.Error("nil embedder should not seed")
	}

	match, _, _ := dc.CheckDuplicate("test")
	if match != "" {
		t.Error("nil embedder should return no match")
	}
}

func TestDedupCache_DefaultThreshold(t *testing.T) {
	dc := NewDedupCache(nil, nil, 0)
	if dc.threshold != 0.85 {
		t.Errorf("default threshold = %.2f, want 0.85", dc.threshold)
	}
}

// countingEmbedder wraps mockEmbedder with a call counter (plan T1: the
// existing mock does not count, and the fallback assertion needs it).
type countingEmbedder struct {
	mockEmbedder
	calls int
}

func (m *countingEmbedder) Embed(text string) ([]float32, error) {
	m.calls++
	return m.mockEmbedder.Embed(text)
}

// captureLog rebinds internal/log's handler to a pipe (os.Stderr
// reassignment + SetVerbosity rebind — internal/log has no capture hook;
// plan T1 mechanism). Process-global: do NOT run parallel. Always uses and
// restores the package default verbosity 0 — internal/log has no level
// getter, so do NOT parameterize the level (a non-default value would leak
// into the rest of the package's tests).
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	log.SetVerbosity(0)
	defer func() {
		os.Stderr = old
		log.SetVerbosity(0)
	}()

	fn()

	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestDedupCache_Seed_VecStoreErrorFallsBackToEmbed pins D2 (REL-04): a
// real vecStore.Get error (closed DB) must NOT be silently swallowed as a
// cache miss — it logs (bounded: first failure + end-of-Seed summary, never
// per-name) and STILL embeds every name correctly.
func TestDedupCache_Seed_VecStoreErrorFallsBackToEmbed(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	vecStore := vectors.NewStore(db)
	db.Close() // every Get now errors (REL-04: real error, not a miss)

	names := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	emb := &countingEmbedder{}
	dc := NewDedupCache(emb, vecStore, 0.85)

	out := captureLog(t, func() {
		dc.Seed(names)
	})

	// Fallback correctness: every name embedded via the API.
	if emb.calls != len(names) {
		t.Errorf("embed calls = %d, want %d (one per name on cache failure)", emb.calls, len(names))
	}
	for _, n := range names {
		if _, _, vec := dc.CheckDuplicate(n); vec == nil {
			t.Errorf("name %q missing from cache after fallback", n)
		}
	}

	// Bounded logging: count lines mentioning the cache-read failure.
	var warnLines int
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "vector cache") && strings.Contains(line, "embed") {
			warnLines++
		}
	}
	if warnLines == 0 {
		t.Error("no cache-read-failure warning logged — real DB error was silently swallowed")
	}
	if warnLines > 2 {
		t.Errorf("cache-read-failure warnings = %d, want ≤ 2 (first failure + Seed summary, no per-name flood):\n%s", warnLines, out)
	}
}
