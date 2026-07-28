package search

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// V-M2 (accumulation): a doc hit at BOTH doc- and chunk-granularity
// accumulates both contributions and outranks a doc with only the chunk hit
// (spec §2.2 — agreement across granularities is signal).
func TestFusionDocAndChunkLegsAccumulate(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	// docA: entry content AND chunk match the query → doc-FTS + chunk-FTS.
	ms.Add(memory.Entry{ID: "docA", Content: "zebra migration patterns"})
	indexTestChunks(t, db, cs, "docA", []memory.ChunkEntry{
		{ChunkID: "docA:c0", ChunkIndex: 0, Content: "zebra migration patterns in detail"},
	})
	// docB: only its chunk matches; entry content is lexically disjoint.
	ms.Add(memory.Entry{ID: "docB", Content: "completely unrelated words here"})
	indexTestChunks(t, db, cs, "docB", []memory.ChunkEntry{
		{ChunkID: "docB:c0", ChunkIndex: 0, Content: "zebra migration patterns appendix"},
	})

	resp, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs}, Request{Query: "zebra migration", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) < 2 {
		t.Fatalf("expected both docs, got %+v", resp.Results)
	}
	if resp.Results[0].DocID != "docA" {
		t.Errorf("docA (doc+chunk legs) must outrank docB (chunk only), got %s first", resp.Results[0].DocID)
	}
	if resp.Results[0].RRFScore <= resp.Results[1].RRFScore {
		t.Errorf("accumulated raw score must exceed single-leg: %v <= %v",
			resp.Results[0].RRFScore, resp.Results[1].RRFScore)
	}
}

// V-M2 (weights): flipping the configured weights flips the order of a
// bm25-only vs a vector-only doc — the weight is observably applied.
func TestFusionWeightsApplied(t *testing.T) {
	build := func(t *testing.T) (Deps, func(bm25W, vecW float64) []SearchResult) {
		db := openTestDB(t)
		cs := memory.NewChunkStore(db)
		ms := memory.NewStore(db)
		vs := vectors.NewStore(db)

		ms.Add(memory.Entry{ID: "lex", Content: "keyword topic material"})
		indexTestChunks(t, db, cs, "lex", []memory.ChunkEntry{
			{ChunkID: "lex:c0", ChunkIndex: 0, Content: "keyword topic material"},
		})
		ms.Add(memory.Entry{ID: "vec", Content: "Vulpes vulpes exhibits adaptability"})
		indexTestChunks(t, db, cs, "vec", []memory.ChunkEntry{
			{ChunkID: "vec:c0", ChunkIndex: 0, Content: "Vulpes vulpes exhibits adaptability"},
		})
		if err := db.WriteTx(func(tx *sql.Tx) error {
			if err := vs.UpsertChunk(tx, "vec:c0", "vec", []float32{1, 0, 0}); err != nil {
				return err
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := vs.Upsert("vec", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		deps := Deps{Mem: ms, Chunks: cs, Vec: vs, Embedder: fixedEmbedder{v: []float32{1, 0, 0}}}
		return deps, func(bm25W, vecW float64) []SearchResult {
			d := deps
			d.BM25Weight, d.VectorWeight = bm25W, vecW
			resp, err := Run(context.Background(), d, Request{Query: "keyword topic", Limit: 5})
			if err != nil {
				t.Fatal(err)
			}
			return resp.Results
		}
	}

	_, runWith := build(t)

	heavy := runWith(0.9, 0.1)
	if len(heavy) < 2 || heavy[0].DocID != "lex" {
		t.Fatalf("bm25-heavy weights: want lex first, got %+v", heavy)
	}
	flipped := runWith(0.1, 0.9)
	if len(flipped) < 2 || flipped[0].DocID != "vec" {
		t.Fatalf("vector-heavy weights: want vec first, got %+v", flipped)
	}
}

// V-M2c: two query-vector lists both ranking doc D SUM their RRF
// contributions (parity with the BM25 side) — the old code took only the
// best (min) rank across vector variants.
func TestFuseLegsSumsMultiQueryVectorLists(t *testing.T) {
	lists := []legList{
		{channel: ChannelVector, hits: []legHit{{docID: "D", chunkID: "D:c0"}, {docID: "E", chunkID: "E:c0"}}},
		{channel: ChannelVector, hits: []legHit{{docID: "D", chunkID: "D:c0"}}},
	}
	fused := fuseLegs(lists, 0.7, 0.3, 0.2)
	var d, e fusedChunk
	for _, fc := range fused {
		switch fc.docID {
		case "D":
			d = fc
		case "E":
			e = fc
		}
	}
	wantD := 0.3/61.0 + 0.3/61.0 // rank 1 in both lists — summed
	if diff := d.rrfScore - wantD; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("doc D score = %v, want %v (contributions must SUM across variant lists)", d.rrfScore, wantD)
	}
	wantE := 0.3 / 62.0 // rank 2 in one list
	if diff := e.rrfScore - wantE; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("doc E score = %v, want %v", e.rrfScore, wantE)
	}
	if fused[0].docID != "D" {
		t.Errorf("summed doc D must rank first, got %s", fused[0].docID)
	}
}

// Spec §4 ablation: nil embedder must be byte-identical to channels:[bm25]
// with an embedder — degraded dependency ≡ explicit lexical-only.
func TestRunNilEmbedderEqualsBM25Channel(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "goroutines are lightweight threads"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "goroutines enable concurrency"},
	})
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return vs.UpsertChunk(tx, "doc1:c0", "doc1", []float32{1, 0, 0})
	}); err != nil {
		t.Fatal(err)
	}

	req := Request{Query: "goroutines", Limit: 5}
	nilEmb, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs}, req)
	if err != nil {
		t.Fatal(err)
	}
	reqBM25 := req
	reqBM25.Channels = []Channel{ChannelBM25}
	bm25Only, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs, Embedder: fixedEmbedder{v: []float32{1, 0, 0}}}, reqBM25)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(nilEmb.Results, bm25Only.Results) {
		t.Errorf("nil-embedder and bm25-channel results diverge:\n nil: %+v\nbm25: %+v",
			nilEmb.Results, bm25Only.Results)
	}
	if len(nilEmb.Results) == 0 {
		t.Fatal("vacuous comparison — no results")
	}
}
