package config

import "testing"

func TestDeepMergePartialNested(t *testing.T) {
	base := map[string]any{
		"compiler": map[string]any{
			"max_parallel":       8,
			"summary_max_tokens": 8000,
			"auto_commit":        true,
		},
	}
	child := map[string]any{
		"compiler": map[string]any{
			"auto_commit": false,
		},
	}
	merged := deepMerge(base, child)
	compiler := merged["compiler"].(map[string]any)
	if compiler["max_parallel"] != 8 {
		t.Errorf("max_parallel: got %v, want 8", compiler["max_parallel"])
	}
	if compiler["summary_max_tokens"] != 8000 {
		t.Errorf("summary_max_tokens: got %v, want 8000", compiler["summary_max_tokens"])
	}
	if compiler["auto_commit"] != false {
		t.Errorf("auto_commit: got %v, want false", compiler["auto_commit"])
	}
}

func TestDeepMergeScalarReplace(t *testing.T) {
	base := map[string]any{"project": "base", "version": 1}
	child := map[string]any{"project": "child"}
	merged := deepMerge(base, child)
	if merged["project"] != "child" {
		t.Errorf("project: got %v, want child", merged["project"])
	}
	if merged["version"] != 1 {
		t.Errorf("version: got %v, want 1", merged["version"])
	}
}
