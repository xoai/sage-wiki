package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildSmokeWS(t *testing.T) (string, func()) {
	t.Helper()
	corpus := t.TempDir()
	for name, body := range map[string]string{
		"a.md": "# Alpha\n\nAlpha discusses beta systems and gamma rays in detail.\n",
		"b.md": "# Beta\n\nBeta systems handle gamma ray processing pipelines.\n",
	} {
		if err := os.WriteFile(filepath.Join(corpus, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origin := NewOriginServer()
	dir := filepath.Join(t.TempDir(), "ws")
	if err := BuildWorkspace(corpus, dir, origin.URL, ""); err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	return dir, origin.Close
}

func TestByteAndGraphParityRoundTrip(t *testing.T) {
	dir, cleanup := buildSmokeWS(t)
	defer cleanup()

	goldenDir := t.TempDir()
	cfg := filepath.Join(goldenDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("# test weights\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Regen requires the FORCE guard.
	if err := RegenGoldens(dir, cfg, goldenDir); err == nil {
		t.Fatal("regen without FORCE must refuse")
	}
	t.Setenv("SAGE_PARITY_FORCE", "1")
	if err := RegenGoldens(dir, cfg, goldenDir); err != nil {
		t.Fatalf("regen: %v", err)
	}

	// Green against freshly generated goldens.
	if err := CheckByteParity(dir, cfg, filepath.Join(goldenDir, "byte-parity.json")); err != nil {
		t.Errorf("byte parity should pass vs fresh golden: %v", err)
	}
	if err := CheckGraphJSONL(dir, filepath.Join(goldenDir, "graph.jsonl")); err != nil {
		t.Errorf("graph parity should pass vs fresh golden: %v", err)
	}

	// Mutation fails, naming the file.
	target := filepath.Join(dir, "wiki", "summaries", "raw-a.md")
	data, err := os.ReadFile(target)
	if err != nil {
		// summaries may be named differently; pick any wiki file
		entries, _ := os.ReadDir(filepath.Join(dir, "wiki", "summaries"))
		if len(entries) == 0 {
			t.Skip("no summaries to mutate")
		}
		target = filepath.Join(dir, "wiki", "summaries", entries[0].Name())
		data, _ = os.ReadFile(target)
	}
	if err := os.WriteFile(target, append(data, 'X'), 0o644); err != nil {
		t.Fatal(err)
	}
	err = CheckByteParity(dir, cfg, filepath.Join(goldenDir, "byte-parity.json"))
	if err == nil {
		t.Fatal("mutation must fail byte parity")
	}
	rel, _ := filepath.Rel(filepath.Join(dir, "wiki"), target)
	if !strings.Contains(err.Error(), filepath.ToSlash(rel)) {
		t.Errorf("failure must name the file %s:\n%v", rel, err)
	}
}
