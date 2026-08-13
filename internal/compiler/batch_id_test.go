package compiler

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBatchIDForPath_DeterministicAndShort verifies the wire-level custom_id
// generator produces stable IDs that fit Zhipu GLM's 64-char limit. Issue #89.
func TestBatchIDForPath_DeterministicAndShort(t *testing.T) {
	paths := []string{
		"raw/article.md",
		"domains/very-long-domain-name/raw/articles/2026-05-31-yet-another-deeply-nested-slug.md",
		"a", // pathological short
		strings.Repeat("very/long/", 100) + "file.md", // pathological long
	}

	for _, p := range paths {
		id := batchIDForPath(p)
		if got := len(id); got != 16 {
			t.Errorf("batchIDForPath(%q): len = %d, want 16", p, got)
		}
		if len(id) > 64 {
			t.Errorf("batchIDForPath(%q): %d chars exceeds GLM 64-char limit", p, len(id))
		}
		// determinism
		if batchIDForPath(p) != id {
			t.Errorf("batchIDForPath(%q): not deterministic", p)
		}
	}

	// Distinct paths yield distinct IDs (sanity, not a real collision check)
	if batchIDForPath("a") == batchIDForPath("b") {
		t.Error("batchIDForPath: distinct paths should yield distinct IDs in trivial case")
	}
}

// TestBatchState_PathByID_JSONRoundTrip verifies the new PathByID map
// survives a checkpoint save/load cycle. submitBatch persists it; resumeBatch
// reads it back from disk.
func TestBatchState_PathByID_JSONRoundTrip(t *testing.T) {
	original := &BatchState{
		BatchID:     "batch_abc123",
		Provider:    "openai-compatible",
		Pass:        "summarize",
		SubmittedAt: "2026-06-01T00:00:00Z",
		PathByID: map[string]string{
			"abc123": "raw/article-one.md",
			"def456": "domains/research/raw/papers/very-long-paper-title.md",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded BatchState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.PathByID) != 2 {
		t.Fatalf("PathByID size = %d, want 2", len(decoded.PathByID))
	}
	if decoded.PathByID["abc123"] != "raw/article-one.md" {
		t.Errorf("path lookup mismatch: got %q", decoded.PathByID["abc123"])
	}
	if decoded.PathByID["def456"] != "domains/research/raw/papers/very-long-paper-title.md" {
		t.Errorf("path lookup mismatch: got %q", decoded.PathByID["def456"])
	}
}

// TestBatchState_PathByID_OmitEmpty verifies a checkpoint without PathByID
// (legacy pre-fix shape) serializes without the field, and round-trips back
// to nil so resumeBatch's fallback path triggers (treats CustomID as path).
func TestBatchState_PathByID_OmitEmpty(t *testing.T) {
	legacy := &BatchState{
		BatchID:     "batch_legacy",
		Provider:    "openai",
		Pass:        "summarize",
		SubmittedAt: "2026-06-01T00:00:00Z",
	}

	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(data), "path_by_id") {
		t.Errorf("empty PathByID should be omitted; got: %s", data)
	}

	var decoded BatchState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.PathByID != nil {
		t.Errorf("legacy checkpoint should decode to nil PathByID, got %v", decoded.PathByID)
	}
}
