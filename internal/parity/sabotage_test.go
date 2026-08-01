package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/compiler"
)

// TestSabotage_MapOrderFailsByteParity is AC-P2 verbatim: shuffling the
// pipeline's source ordering (a map-iteration-class leak) must fail byte
// parity with a readable diff — and unsetting the seam restores green.
func TestSabotage_MapOrderFailsByteParity(t *testing.T) {
	// Deterministic reversal (not random — the test itself must not flake).
	compiler.ShuffleSourcesForTest = func(in []compiler.SourceInfo) []compiler.SourceInfo {
		out := make([]compiler.SourceInfo, len(in))
		for i, s := range in {
			out[len(in)-1-i] = s
		}
		return out
	}
	defer func() { compiler.ShuffleSourcesForTest = nil }()

	replay, err := NewReplayServer(filepath.Join("..", "..", "testdata", "fixtures", "openai"))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	ws := filepath.Join(t.TempDir(), "ws")
	if err := BuildWorkspace(filepath.Join("..", "..", "testdata", "golden-corpus"), ws, replay.URL(), readGoldenConfigForSabotage(t)); err != nil {
		t.Fatalf("sabotaged build: %v", err)
	}

	err = CheckByteParity(ws, goldenConfigPath(), goldenPath("byte-parity.json"))
	if err == nil {
		t.Fatal("sabotaged source order must fail byte parity")
	}
	// AC-P2 verbatim: the failure must name a concrete file.
	if !strings.Contains(err.Error(), ".md") {
		t.Fatalf("failure must name a concrete file: %v", err)
	}
	t.Logf("byte parity caught the sabotage:\n%v", err)

	compiler.ShuffleSourcesForTest = nil
	ws2 := filepath.Join(t.TempDir(), "ws2")
	if err := BuildWorkspace(filepath.Join("..", "..", "testdata", "golden-corpus"), ws2, replay.URL(), readGoldenConfigForSabotage(t)); err != nil {
		t.Fatalf("clean build: %v", err)
	}
	if err := CheckByteParity(ws2, goldenConfigPath(), goldenPath("byte-parity.json")); err != nil {
		t.Fatalf("clean build must pass after unsetting the seam: %v", err)
	}
}

func readGoldenConfigForSabotage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(goldenConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
