package parity

import (
	"database/sql"
	"path/filepath"
	"testing"

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
	db, err := sql.Open("sqlite", filepath.Join(sageDir, "wiki.db"))
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
}
