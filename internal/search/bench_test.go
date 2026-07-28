package search

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/ontology"

	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

func openBenchDB(b testing.TB) *storage.DB {
	b.Helper()
	dir := b.TempDir()
	db, err := storage.Open(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}

// benchCorpus builds a ~1k-entry corpus with chunks and vectors — the M2
// interim latency tripwire fixture (plan M2 exit; V-M5c's eventual corpus).
func benchCorpus(b testing.TB) (Deps, *hybrid.Searcher) {
	b.Helper()
	db := openBenchDB(b)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("concept:topic%d", i)
		content := fmt.Sprintf("topic%d covers subject matter %d with details on area%d and notes", i, i, i%37)
		ms.Add(memory.Entry{ID: id, Content: content, ArticlePath: fmt.Sprintf("wiki/concepts/topic%d.md", i)})
		if err := db.WriteTx(func(tx *sql.Tx) error {
			if err := cs.IndexChunks(tx, id, []memory.ChunkEntry{
				{ChunkID: id + ":c0", ChunkIndex: 0, Content: content},
			}); err != nil {
				return err
			}
			vec := []float32{float32(i%97) / 97.0, float32(i%53) / 53.0, float32(i%29) / 29.0}
			if err := vs.UpsertChunk(tx, id+":c0", id, vec); err != nil {
				return err
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		vec := []float32{float32(i%97) / 97.0, float32(i%53) / 53.0, float32(i%29) / 29.0}
		if err := vs.Upsert(id, vec); err != nil {
			b.Fatal(err)
		}
	}
	// Shaped like a shipped adapter, not like a minimal facade call: every
	// entry point passes a non-nil IncludeDoc (trust.IncludePredicate always
	// returns a closure) and an ontology store, and both change the work Run
	// does — the trust predicate triples the pipeline limit, the ontology
	// store adds an EntityCount probe per query. Measuring without them
	// would benchmark a configuration that ships nowhere.
	ont := ontology.NewStore(db, nil, nil)
	return Deps{
			Mem: ms, Chunks: cs, Vec: vs,
			Embedder:   fixedEmbedder{v: []float32{0.5, 0.5, 0.5}},
			Ont:        ont,
			IncludeDoc: func(docID string) bool { return !strings.HasPrefix(docID, "output:") },
		},
		hybrid.NewSearcher(ms, vs)
}

// BenchmarkRunUnified measures the unified facade (LLM stages off) on the
// 1k corpus — the "after" side of the V-M5c comparison.
func BenchmarkRunUnified(b *testing.B) {
	deps, _ := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Run(deps, Request{Query: fmt.Sprintf("topic%d subject details", i%1000), Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHybridDocPath measures the legacy doc-level path on the same
// corpus — the baseline side.
func BenchmarkHybridDocPath(b *testing.B) {
	deps, searcher := benchCorpus(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := fmt.Sprintf("topic%d subject details", i%1000)
		qv, _ := deps.Embedder.Embed(q)
		if _, err := searcher.Search(hybrid.SearchOpts{Query: q, Limit: 10, BM25Weight: 0.7, VectorWeight: 0.3}, qv); err != nil {
			b.Fatal(err)
		}
	}
}
