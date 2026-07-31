package search

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"

	"github.com/xoai/sage-wiki/internal/hybrid"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// encodeVec mirrors vectors.encodeFloat32s (little-endian float32 blob) —
// the fixture inserts vectors by raw SQL inside its single WriteTx.
func encodeVec(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

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
//
// The whole fixture builds inside ONE WriteTx: ~6000 individual
// transactions (entries, chunk rows, vectors, entities, relations) was the
// dominant cost of this package under -race (234s in CI). Raw inserts are
// safe here because the DB is fresh (no upsert semantics needed) and both
// vector caches lazy-load from the DB on first search.
func benchCorpus(b testing.TB) (Deps, *hybrid.Searcher) {
	b.Helper()
	db := openBenchDB(b)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)
	ont := ontology.NewStore(db, nil, nil)

	if err := db.WriteTx(func(tx *sql.Tx) error {
		for i := 0; i < 1000; i++ {
			id := fmt.Sprintf("concept:topic%d", i)
			content := fmt.Sprintf("topic%d covers subject matter %d with details on area%d and notes", i, i, i%37)
			articlePath := fmt.Sprintf("wiki/concepts/topic%d.md", i)
			if _, err := tx.Exec(
				"INSERT INTO entries (id, content, tags, article_path) VALUES (?, ?, ?, ?)",
				id, content, "", articlePath); err != nil {
				return err
			}
			if err := cs.IndexChunks(tx, id, []memory.ChunkEntry{
				{ChunkID: id + ":c0", ChunkIndex: 0, Content: content},
			}); err != nil {
				return err
			}
			vec := []float32{float32(i%97) / 97.0, float32(i%53) / 53.0, float32(i%29) / 29.0}
			if err := vs.UpsertChunk(tx, id+":c0", id, vec); err != nil {
				return err
			}
			if _, err := tx.Exec(
				`INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)`,
				id, encodeVec(vec), len(vec)); err != nil {
				return err
			}

			entID := fmt.Sprintf("topic%d", i)
			now := time.Now().UTC().Format(time.RFC3339)
			if _, err := tx.Exec(
				`INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				entID, string(ontology.TypeConcept), entID, "",
				fmt.Sprintf("wiki/concepts/%s.md", entID), now, now); err != nil {
				return err
			}
		}
		// Relations in a second pass: their FK targets must already exist.
		now := time.Now().UTC().Format(time.RFC3339)
		for i := 0; i < 1000; i++ {
			for j := 1; j <= 3; j++ {
				target := (i + j*7) % 1000
				if _, err := tx.Exec(
					`INSERT INTO relations (id, source_id, target_id, relation, created_at,
					                        evidence, confidence, source_doc,
					                        valid_from, valid_to, invalidated_by)
					 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					fmt.Sprintf("r%d-%d", i, j), fmt.Sprintf("topic%d", i),
					fmt.Sprintf("topic%d", target), string(ontology.RelCites), now,
					"", 0, "", "", "", ""); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	// Shaped like a shipped adapter, not like a minimal facade call: every
	// entry point passes a non-nil IncludeDoc (trust.IncludePredicate always
	// returns a closure) and an ontology store, and both change the work Run
	// does — the trust predicate triples the pipeline limit, the ontology
	// store adds an EntityCount probe per query. The ontology is POPULATED
	// for the same reason: with zero entities the EntityCount fast path
	// skips buildGraphLeg entirely, so an empty-store fixture measures a
	// two-channel pipeline while three-channel ships.
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
		if _, err := Run(context.Background(), deps, Request{Query: fmt.Sprintf("topic%d subject details", i%1000), Limit: 10}); err != nil {
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
