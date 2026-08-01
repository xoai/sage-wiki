package parity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/search"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// TestMmapParity_GoldenCorpus is SPEC-06's headline parity gate: the fp32
// mmap backend must return results IDENTICAL to the recorded golden (which
// the in-memory backend satisfies). The index files are rebuilt from the
// suite workspace's persisted embeddings, then the full search golden runs
// through the mmap path.
func TestMmapParity_GoldenCorpus(t *testing.T) {
	if suiteWS == "" {
		t.Skip("parity suite workspace unavailable (SAGE_PARITY_FORCE?)")
	}
	sageDir := filepath.Join(suiteWS, ".sage")
	db, err := storage.Open(filepath.Join(sageDir, "wiki.db"))
	if err != nil {
		t.Fatalf("open suite db: %v", err)
	}
	defer db.Close()
	if _, err := vectors.WriteIndexFile(db, vectors.IndexTableDocs, filepath.Join(sageDir, "vectors.idx"), vectors.QuantNone); err != nil {
		t.Fatalf("rebuild doc index: %v", err)
	}
	if _, err := vectors.WriteIndexFile(db, vectors.IndexTableChunks, filepath.Join(sageDir, "vectors-chunks.idx"), vectors.QuantNone); err != nil {
		t.Fatalf("rebuild chunk index: %v", err)
	}

	if err := CheckSearchParityOpts(suiteWS, goldenPath("search.json"),
		vectors.WithVectorBackend("mmap"), vectors.WithIndexDir(sageDir)); err != nil {
		t.Errorf("mmap golden parity: %v", err)
	}

	// Anti-vacuity (F-052): the golden passing could hide a silent
	// fallback to the memory path. Run one golden query through the real
	// search pipeline with the mmap options and assert the snapshot
	// SERVED (and the answer still matches the golden).
	sg := readSearchGoldenFile(t, goldenPath("search.json"))
	if len(sg.Queries) == 0 {
		t.Fatal("search golden has no queries")
	}
	deps, db2, err := searchDeps(suiteWS,
		vectors.WithVectorBackend("mmap"), vectors.WithIndexDir(sageDir))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	q := sg.Queries[0]
	resp, err := search.Run(context.Background(), deps, search.Request{Query: q.Q, Limit: 10, Granularity: search.Docs})
	if err != nil {
		t.Fatalf("pipeline search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Error("pipeline search returned no results")
	}
	vs, ok := deps.Vec.(*vectors.Store)
	if !ok {
		t.Fatalf("deps.Vec is %T, want *vectors.Store", deps.Vec)
	}
	if served := vs.MmapServedCount(); served == 0 {
		t.Error("MmapServedCount = 0 — the pipeline fell back to memory; the golden gate is vacuous")
	}
}

// readSearchGoldenFile loads the search golden (test helper).
func readSearchGoldenFile(t *testing.T, path string) SearchGolden {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sg SearchGolden
	if err := json.Unmarshal(raw, &sg); err != nil {
		t.Fatal(err)
	}
	return sg
}
