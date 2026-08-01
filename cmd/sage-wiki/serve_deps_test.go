package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/serve"
	"github.com/xoai/sage-wiki/internal/wiki"
)

func TestAssembleServeDeps_WorkerStartedWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	deps, err := serve.AssembleDeps(dir)
	if err != nil {
		t.Fatalf("assembleServeDeps: %v", err)
	}
	defer deps.Close()
	if !deps.WorkerEnabled() {
		t.Error("worker nil with default config — serve.worker defaults to enabled")
	}
	if deps.Coordinator() == nil || deps.Progress() == nil {
		t.Error("shared coordinator/progress not constructed")
	}
}

func TestAssembleServeDeps_WorkerDisabledByConfig(t *testing.T) {
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")

	// Explicit opt-out (full rewrite — the scaffold already has a serve section).
	cfgContent := `version: 1
project: test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
serve:
  worker:
    enabled: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	deps, err := serve.AssembleDeps(dir)
	if err != nil {
		t.Fatalf("assembleServeDeps: %v", err)
	}
	defer deps.Close()
	if deps.WorkerEnabled() {
		t.Error("worker constructed despite serve.worker.enabled: false")
	}
}
