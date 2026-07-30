package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	collectOnce sync.Once
	cachedData  skillData
)

func testData(t *testing.T) skillData {
	t.Helper()
	collectOnce.Do(func() {
		cachedData = collectData("")
	})
	return cachedData
}

func render(t *testing.T, sd skillData) (ref, pipe string) {
	t.Helper()
	refBuf := &bytes.Buffer{}
	if err := referenceTmpl.Execute(refBuf, sd); err != nil {
		t.Fatalf("reference template: %v", err)
	}
	pipeBuf := &bytes.Buffer{}
	if err := pipelineTmpl.Execute(pipeBuf, sd); err != nil {
		t.Fatalf("pipeline template: %v", err)
	}
	return refBuf.String(), pipeBuf.String()
}

// S-01: regeneration is deterministic — two independent collections render
// byte-identical output (guards the CI git-diff drift check).
func TestRegenerateIdempotent(t *testing.T) {
	sd1 := testData(t)
	sd2 := collectData("")
	ref1, pipe1 := render(t, sd1)
	ref2, pipe2 := render(t, sd2)
	if ref1 != ref2 {
		t.Errorf("reference skill not byte-identical across runs (%d vs %d bytes)", len(ref1), len(ref2))
	}
	if pipe1 != pipe2 {
		t.Errorf("pipeline skill not byte-identical across runs (%d vs %d bytes)", len(pipe1), len(pipe2))
	}
}

// S-02: tiers are 0/1/3 — no standalone Tier 2 claim.
func TestTiersZeroOneThree(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "0 / 1 / 3") {
		t.Error("reference skill missing tiers 0 / 1 / 3 statement")
	}
	if !strings.Contains(ref, "no Tier 2") {
		t.Error("reference skill missing explicit no-Tier-2 statement")
	}
}

// S-03: temporal default is true; the other three flags default false.
func TestTemporalDefaultTrue(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "ontology.temporal.enabled") {
		t.Fatal("reference skill missing ontology.temporal.enabled flag")
	}
	for _, f := range testData(t).OptInFlags {
		if f.Flag == "ontology.temporal.enabled" && f.Default != "true" {
			t.Errorf("temporal default = %q, want true", f.Default)
		}
		if f.Flag != "ontology.temporal.enabled" && f.Default != "false" {
			t.Errorf("%s default = %q, want false", f.Flag, f.Default)
		}
	}
}

// S-04: async compile documented — 202 + job_id present.
func TestAsyncCompileDocumented(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "202") || !strings.Contains(ref, "job_id") {
		t.Error("reference skill missing async compile documentation (202 + job_id)")
	}
}

// S-05: tool count matches the registry (18).
func TestToolCount(t *testing.T) {
	sd := testData(t)
	if sd.ToolCount != 18 {
		t.Errorf("ToolCount = %d, want 18", sd.ToolCount)
	}
	if len(sd.Tools) != sd.ToolCount {
		t.Errorf("len(Tools) = %d, ToolCount = %d — mismatch", len(sd.Tools), sd.ToolCount)
	}
}

// S-06: every registry tool appears in the generated reference skill by name.
func TestEveryToolAppears(t *testing.T) {
	ref, _ := render(t, testData(t))
	for _, te := range testData(t).Tools {
		if !strings.Contains(ref, "`"+te.Name+"`") {
			t.Errorf("tool %s missing from generated reference skill", te.Name)
		}
	}
}

// S-07: rendered output matches the committed files (local mirror of the CI
// git-diff drift check).
func TestOutputMatchesCommitted(t *testing.T) {
	ref, pipe := render(t, testData(t))
	root := filepath.Join("..", "..")
	committedRef, err := os.ReadFile(filepath.Join(root, "skills", "sage-wiki", "SKILL.md"))
	if err != nil {
		t.Fatalf("read committed reference skill: %v", err)
	}
	if string(committedRef) != ref {
		t.Error("skills/sage-wiki/SKILL.md is stale — run: go run ./tools/skillgen/")
	}
	committedPipe, err := os.ReadFile(filepath.Join(root, "skills", "sage-wiki-integrate", "SKILL.md"))
	if err != nil {
		t.Fatalf("read committed pipeline skill: %v", err)
	}
	if string(committedPipe) != pipe {
		t.Error("skills/sage-wiki-integrate/SKILL.md is stale — run: go run ./tools/skillgen/")
	}
}

// S-08: connectivity documentation present.
func TestHowToConnect(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "How to connect") {
		t.Error("reference skill missing How to connect section")
	}
}

// S-09: error-code vocabulary table present.
func TestErrorCodes(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "Error Codes") {
		t.Fatal("reference skill missing Error Codes section")
	}
	for _, ec := range testData(t).ErrorCodes {
		if !strings.Contains(ref, ec.Code) {
			t.Errorf("error code %s missing from reference skill", ec.Code)
		}
	}
}

// S-10: opt-in features section names all four flags.
func TestOptInFeatures(t *testing.T) {
	ref, _ := render(t, testData(t))
	if !strings.Contains(ref, "Opt-In Features") {
		t.Fatal("reference skill missing Opt-In Features section")
	}
	for _, f := range testData(t).OptInFlags {
		if !strings.Contains(ref, f.Flag) {
			t.Errorf("opt-in flag %s missing from reference skill", f.Flag)
		}
	}
}

// S-11: pipeline skill covers language detection + smoke test.
func TestPipelineContent(t *testing.T) {
	_, pipe := render(t, testData(t))
	for _, want := range []string{"smoke test", "pyproject.toml", "package.json"} {
		if !strings.Contains(pipe, want) {
			t.Errorf("pipeline skill missing %q", want)
		}
	}
}

// S-12: pipeline skill has the MCP-config fallback for non-Python/TS repos.
func TestPipelineMCPFallback(t *testing.T) {
	_, pipe := render(t, testData(t))
	if !strings.Contains(pipe, "MCP config") {
		t.Error("pipeline skill missing MCP config fallback path")
	}
}
