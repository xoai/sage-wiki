package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTemporalDefaults(t *testing.T) {
	t.Run("zero value means enabled with 0.8 threshold", func(t *testing.T) {
		tc := TemporalConfig{}
		if !tc.EnabledOrDefault() {
			t.Error("EnabledOrDefault() = false, want true (default)")
		}
		if got := tc.AutoApplyThresholdOrDefault(); got != 0.8 {
			t.Errorf("AutoApplyThresholdOrDefault() = %v, want 0.8", got)
		}
	})
	t.Run("explicit false disables", func(t *testing.T) {
		f := false
		tc := TemporalConfig{Enabled: &f}
		if tc.EnabledOrDefault() {
			t.Error("EnabledOrDefault() = true, want false")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		tr := true
		tc := TemporalConfig{Enabled: &tr}
		if !tc.EnabledOrDefault() {
			t.Error("EnabledOrDefault() = false, want true")
		}
	})
	t.Run("threshold override honored, out-of-range falls back", func(t *testing.T) {
		tc := TemporalConfig{AutoApplyThreshold: 0.95}
		if got := tc.AutoApplyThresholdOrDefault(); got != 0.95 {
			t.Errorf("got %v, want 0.95", got)
		}
		for _, bad := range []float64{-0.1, 0, 1.1, 2} {
			tc := TemporalConfig{AutoApplyThreshold: bad}
			if got := tc.AutoApplyThresholdOrDefault(); got != 0.8 {
				t.Errorf("threshold %v: got %v, want fallback 0.8", bad, got)
			}
		}
	})
}

func TestRelationConfigFunctional(t *testing.T) {
	rc := RelationConfig{Name: "works_at", Functional: true}
	if !rc.Functional {
		t.Error("Functional not set")
	}
	def := RelationConfig{Name: "related_to"}
	if def.Functional {
		t.Error("Functional must default to false")
	}
}

func TestTemporalYamlRoundTrip(t *testing.T) {
	yamlDoc := []byte(`project: test
output: wiki
sources:
  - path: raw
ontology:
  temporal:
    enabled: false
    auto_apply_threshold: 0.9
  relations:
    - name: works_at
      functional: true
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, yamlDoc, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Ontology.Temporal.EnabledOrDefault() {
		t.Error("temporal.enabled parsed as true, want false")
	}
	if got := cfg.Ontology.Temporal.AutoApplyThresholdOrDefault(); got != 0.9 {
		t.Errorf("threshold = %v, want 0.9", got)
	}
	if len(cfg.Ontology.Relations) != 1 || !cfg.Ontology.Relations[0].Functional {
		t.Errorf("functional relation not parsed: %+v", cfg.Ontology.Relations)
	}
}
