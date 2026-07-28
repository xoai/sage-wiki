package search

import (
	"context"
	"database/sql"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// fixedEmbedder returns the same vector for every input.
type fixedEmbedder struct{ v []float32 }

func (f fixedEmbedder) Embed(string) ([]float32, error) { return f.v, nil }
func (f fixedEmbedder) Dimensions() int                 { return len(f.v) }
func (f fixedEmbedder) Name() string                    { return "fixed" }

// V-M1c: a chunk found ONLY by vector search must reach the results (and
// any reranker) with its real heading and content — not the empty strings
// the fusion map used to leave behind for vector-only hits.
func TestEnhancedSearch_VectorOnlyChunkIsHydrated(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "Vulpes vulpes exhibits remarkable adaptability"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Heading: "Foxes",
			Content: "Vulpes vulpes exhibits remarkable adaptability"},
	})
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return vs.UpsertChunk(tx, "doc1:c0", "doc1", []float32{1, 0, 0})
	}); err != nil {
		t.Fatalf("upsert chunk vector: %v", err)
	}

	// "sly cunning" shares no stemmed term with the chunk text, so BM25
	// cannot find it; only the vector leg can.
	results, err := EnhancedSearch(context.Background(), EnhancedSearchOpts{
		Query:          "sly cunning",
		Limit:          5,
		Embedder:       fixedEmbedder{v: []float32{1, 0, 0}},
		ChunkStore:     cs,
		MemStore:       ms,
		VecStore:       vs,
		QueryExpansion: false,
		RerankEnabled:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected a vector-only result")
	}
	r := results[0]
	if r.DocID != "doc1" || r.ChunkID != "doc1:c0" {
		t.Fatalf("unexpected hit: %+v", r)
	}
	if r.ChunkText == "" {
		t.Error("vector-only chunk reached results with empty ChunkText — hydration missing")
	}
	if r.Heading != "Foxes" {
		t.Errorf("vector-only chunk heading = %q, want %q", r.Heading, "Foxes")
	}
}

// A chunk hit whose chunks_meta row is missing (a stale chunk-vector cache
// across a reindex) used to fall between the two hydration conditions: chunk
// hydration found nothing, and the entry fallback only ran for hits with no
// chunk ID at all. The passage then reached the reranker — and the query
// path's context — blank.
func TestHydrationCoversChunkHitsWithNoChunkRow(t *testing.T) {
	db := openTestDB(t)
	ms := memory.NewStore(db)
	cs := memory.NewChunkStore(db)
	vs := vectors.NewStore(db)

	if err := ms.Add(memory.Entry{ID: "doc1", Content: "REAL ENTRY CONTENT about retrieval"}); err != nil {
		t.Fatal(err)
	}
	// A chunk vector pointing at a chunk row that no longer exists.
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return vs.UpsertChunk(tx, "doc1:cGONE", "doc1", []float32{1, 0, 0})
	}); err != nil {
		t.Fatal(err)
	}
	vs.InvalidateChunkCache()

	for _, gran := range []struct {
		name string
		g    Granularity
	}{{"chunks", Chunks}, {"docs", Docs}} {
		t.Run(gran.name, func(t *testing.T) {
			resp, err := Run(context.Background(), Deps{
				Mem: ms, Chunks: cs, Vec: vs,
				Embedder: fixedEmbedder{v: []float32{1, 0, 0}},
			}, Request{Query: "retrieval", Limit: 5, Granularity: gran.g})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(resp.Results) == 0 {
				t.Fatal("no results — fixture drift")
			}
			for _, r := range resp.Results {
				if r.ChunkText == "" {
					t.Errorf("doc %s reached output with a blank passage (chunk %q)", r.DocID, r.ChunkID)
				}
			}
		})
	}
}
