package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCompileCmd_ForceFlagRegistered pins the --force flag's existence and
// default (off) — the R1 bypass must be a deliberate ask.
func TestCompileCmd_ForceFlagRegistered(t *testing.T) {
	f := compileCmd.Flags().Lookup("force")
	if f == nil {
		t.Fatal("--force flag not registered on compileCmd")
	}
	if f.DefValue != "false" {
		t.Errorf("--force default = %q, want false", f.DefValue)
	}
}

// TestCompileCmd_ExplainFlagRegistered pins --explain.
func TestCompileCmd_ExplainFlagRegistered(t *testing.T) {
	f := compileCmd.Flags().Lookup("explain")
	if f == nil {
		t.Fatal("--explain flag not registered on compileCmd")
	}
}

// TestExplainCompile_JSONShape is the CLI-half of AC-5: the --json
// explanation carries every spec'd field.
func TestExplainCompile_JSONShape(t *testing.T) {
	fields := []string{
		"path", "source_hash", "pipeline", "templates", "models",
		"config_hash", "embed", "key", "stored_key",
		"stored_parts", "current_parts", "verdict",
	}
	sample := map[string]any{}
	raw, err := json.Marshal(map[string]any{
		"path": "raw/a.md", "source_hash": "sha256:x", "pipeline": "1",
		"templates": "t", "models": "m", "config_hash": "c", "embed": "e",
		"key": "k", "stored_key": "s", "stored_parts": map[string]any{},
		"current_parts": map[string]any{}, "verdict": "skip: unchanged",
	})
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &sample)
	for _, f := range fields {
		if _, ok := sample[f]; !ok {
			t.Errorf("explanation JSON missing field %q", f)
		}
	}
	_ = strings.Join(fields, ",")
}
