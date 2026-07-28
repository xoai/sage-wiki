package search

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// 2.1a golden: on a fixture corpus, Run(Chunks) must equal the enhanced
// pipeline it re-homes — query's output is unchanged at this boundary.
func TestRunMatchesEnhancedSearchAt21aBoundary(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "goroutines are lightweight threads"})
	ms.Add(memory.Entry{ID: "doc2", Content: "python asyncio event loop"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Heading: "Goroutines", Content: "goroutines enable concurrent programming in go"},
		{ChunkID: "doc1:c1", ChunkIndex: 1, Heading: "Channels", Content: "channels provide communication between goroutines"},
	})
	indexTestChunks(t, db, cs, "doc2", []memory.ChunkEntry{
		{ChunkID: "doc2:c0", ChunkIndex: 0, Heading: "Asyncio", Content: "python asyncio provides event loop for async io"},
	})

	want, err := EnhancedSearch(context.Background(), EnhancedSearchOpts{
		Query: "goroutines concurrent", Limit: 5,
		ChunkStore: cs, MemStore: ms, VecStore: vs,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := Run(
		context.Background(),
		Deps{Mem: ms, Chunks: cs, Vec: vs},
		Request{Query: "goroutines concurrent", Limit: 5, Granularity: Chunks},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Results, want) {
		t.Errorf("Run diverged from the enhanced pipeline at the 2.1a boundary:\n got %+v\nwant %+v", got.Results, want)
	}
	if len(got.Results) == 0 {
		t.Fatal("fixture returned no results — golden is vacuous")
	}
}

// LLM stages are OFF by default: the zero-value Request must run without
// any LLM client and without attempting expansion or rerank.
func TestRunLLMStagesDefaultOff(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "test content"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "test content here"},
	})

	var req Request
	req.Query = "test"
	req.Limit = 5
	if req.Expand || req.Rerank {
		t.Fatal("zero-value Request must have LLM stages off")
	}
	resp, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs, Vec: vs}, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected results with LLM stages off and no client")
	}
}

// Channel plumbing: excluding the vector channel disables the vector legs
// even when an embedder is available (the ablation surface, ADR-036).
func TestRunChannelsExcludeVector(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	// Vector-only doc: no lexical overlap with the query.
	ms.Add(memory.Entry{ID: "doc1", Content: "Vulpes vulpes exhibits remarkable adaptability"})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "Vulpes vulpes exhibits remarkable adaptability"},
	})
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return vs.UpsertChunk(tx, "doc1:c0", "doc1", []float32{1, 0, 0})
	}); err != nil {
		t.Fatalf("upsert chunk vector: %v", err)
	}

	deps := Deps{Mem: ms, Chunks: cs, Vec: vs, Embedder: fixedEmbedder{v: []float32{1, 0, 0}}}

	// Positive control: with all channels the vector leg finds it.
	all, err := Run(context.Background(), deps, Request{Query: "sly cunning", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Results) == 0 {
		t.Fatal("control failed: vector leg did not find the doc with all channels on")
	}

	// Restricted to bm25, the vector-only doc must NOT appear.
	resp, err := Run(context.Background(), deps, Request{Query: "sly cunning", Limit: 5, Channels: []Channel{ChannelBM25}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("bm25-only channels returned vector-only hit: %+v", resp.Results)
	}
}

// A hard tag filter must be pushed INTO retrieval, not merely applied to
// whatever the unfiltered query happened to return. With a post-filter only,
// a tagged document that ranks below the candidate cut for the raw query is
// gone before the filter ever sees it — `--tags rare` then returns nothing on
// any corpus large enough to matter, which is what the legacy doc path did
// NOT do (it passed tags into the FTS query).
func TestRunHardTagFilterSurvivesALargeUntaggedPool(t *testing.T) {
	db := openTestDB(t)
	ms := memory.NewStore(db)
	cs := memory.NewChunkStore(db)

	// 300 strong matches for the query, none carrying the tag.
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("concept:noise%d", i)
		if err := ms.Add(memory.Entry{
			ID:      id,
			Content: "retrieval ranking retrieval ranking retrieval ranking",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// One weak match that carries it.
	if err := ms.Add(memory.Entry{
		ID:      "concept:tagged",
		Content: "a passing mention of retrieval",
		Tags:    []string{"rare"},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := Run(context.Background(), Deps{Mem: ms, Chunks: cs}, Request{
		Query:       "retrieval ranking",
		Limit:       10,
		FilterTags:  []string{"rare"},
		Granularity: Docs,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].DocID != "concept:tagged" {
		got := make([]string, len(resp.Results))
		for i, r := range resp.Results {
			got[i] = r.DocID
		}
		t.Fatalf("tag-filtered search returned %d results (%v), want just concept:tagged",
			len(resp.Results), got)
	}
}
