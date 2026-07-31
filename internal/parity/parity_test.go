package parity

import (
	"os"
	"path/filepath"
	"testing"
)

var suiteWS string

// TestMain builds the shared workspace once (replay mode) for all parity
// checks — the flake guard's double-run lives inside each check.
func TestMain(m *testing.M) {
	root := filepath.Join("..", "..", "testdata")
	replay, err := NewReplayServer(filepath.Join(root, "fixtures", "openai"))
	if err != nil {
		panic(err)
	}
	ws := filepath.Join(os.TempDir(), "parity-suite-ws")
	os.RemoveAll(ws)
	if err := BuildWorkspace(filepath.Join(root, "golden-corpus"), ws, replay.URL(), readGoldenConfigForMain(root)); err != nil {
		panic(err)
	}
	suiteWS = ws
	code := m.Run()
	replay.Close()
	os.RemoveAll(ws)
	os.Exit(code)
}

func readGoldenConfigForMain(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "golden", "config.yaml"))
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "golden", name)
}

func goldenConfigPath() string { return goldenPath("config.yaml") }

// TestParity is the CI flake-loop entry: all four checks in one name so
// `-run TestParity` matches (plan F-014).
func TestParity(t *testing.T) {
	t.Run("byte", func(t *testing.T) {
		if err := CheckByteParity(suiteWS, goldenConfigPath(), goldenPath("byte-parity.json")); err != nil {
			t.Error(err)
		}
	})
	t.Run("graph", func(t *testing.T) {
		if err := CheckGraphJSONL(suiteWS, goldenPath("graph.jsonl")); err != nil {
			t.Error(err)
		}
	})
	t.Run("search", func(t *testing.T) {
		if err := CheckSearchParity(suiteWS, goldenPath("search.json")); err != nil {
			t.Error(err)
		}
	})
	t.Run("roundtrip", func(t *testing.T) {
		if err := CheckRoundTrip(suiteWS, goldenPath("search.json")); err != nil {
			t.Error(err)
		}
	})
}

// TestParityCorruption is AC-P4's integrity proof.
func TestParityCorruption(t *testing.T) {
	if err := CheckRoundTripCorruption(suiteWS, goldenConfigPath()); err != nil {
		t.Error(err)
	}
}
