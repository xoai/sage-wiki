package parity

import (
	"os"
	"path/filepath"
	"testing"
)

var suiteWS string

// TestMain builds the shared workspace once (replay mode) for all parity
// checks — the flake guard's double-run lives inside each check. Under
// SAGE_PARITY_FORCE=1 (record/regen invocations) the build is SKIPPED:
// gen tests build their own workspaces, and a build here would
// replay-miss after any prompt change, deadlocking the documented
// maintainer flow (review R-01).
func TestMain(m *testing.M) {
	force := os.Getenv("SAGE_PARITY_FORCE") == "1"
	var replay *Server
	var ws string
	if !force {
		root := filepath.Join("..", "..", "testdata")
		var err error
		replay, err = NewReplayServer(filepath.Join(root, "fixtures", "openai"))
		if err != nil {
			panic(err)
		}
		// Unique per process — concurrent `go test` runs must never remove
		// each other's workspace.
		ws, err = os.MkdirTemp("", "parity-suite-ws-")
		if err != nil {
			panic(err)
		}
		if err := BuildWorkspace(filepath.Join(root, "golden-corpus"), ws, replay.URL(), readGoldenConfigForMain(root)); err != nil {
			panic(err)
		}
		suiteWS = ws
	}
	code := m.Run()
	if replay != nil {
		replay.Close()
	}
	if ws != "" {
		os.RemoveAll(ws)
	}
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
	if suiteWS == "" {
		t.Skip("shared workspace not built (SAGE_PARITY_FORCE=1 mode)")
	}
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
	t.Run("asof", func(t *testing.T) {
		if err := CheckAsOf(suiteWS, goldenPath("graph-asof.json")); err != nil {
			t.Error(err)
		}
	})
}

// TestParityCorruption is AC-P4's integrity proof.
func TestParityCorruption(t *testing.T) {
	if suiteWS == "" {
		t.Skip("shared workspace not built (SAGE_PARITY_FORCE=1 mode)")
	}
	if err := CheckRoundTripCorruption(suiteWS, goldenConfigPath()); err != nil {
		t.Error(err)
	}
}
