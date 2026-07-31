package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadPreFormatManifestIsV02x: a v0.2.x manifest (no format_version)
// loads and reports IsPreFormat — the SPEC-01 read-only-until-adopted
// discriminator.
func TestLoadPreFormatManifestIsV02x(t *testing.T) {
	m, err := Load("testdata/v02x.manifest.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !m.IsPreFormat() {
		t.Error("manifest without format_version must report IsPreFormat (v0.2.x)")
	}
	if m.Version != 2 || len(m.Sources) != 1 {
		t.Error("existing fields must survive the load")
	}
}

// TestNewStampsFormat: new manifests carry format_version, engine, created_at.
func TestNewStampsFormat(t *testing.T) {
	m := New()
	if m.FormatVersion != CurrentFormatVersion {
		t.Errorf("FormatVersion = %d, want %d", m.FormatVersion, CurrentFormatVersion)
	}
	if m.Engine == "" || m.CreatedAt == "" {
		t.Error("Engine and CreatedAt must be stamped")
	}
	if m.IsPreFormat() {
		t.Error("new manifest must not be pre-format")
	}
}

// TestClonePreservesFormatFields: the merge base must not strip them.
func TestClonePreservesFormatFields(t *testing.T) {
	m := New()
	m.Engine = "9.9.9-test"
	c := m.Clone()
	if c.FormatVersion != m.FormatVersion || c.Engine != "9.9.9-test" || c.CreatedAt != m.CreatedAt {
		t.Error("Clone stripped format fields")
	}
}

// TestPreFormatRoundTripIgnoresNewFields: an OLD binary (struct without the
// new fields) reading a NEW manifest must not fail — encoding/json ignores
// unknown keys both directions.
func TestPreFormatRoundTripIgnoresNewFields(t *testing.T) {
	type oldManifest struct {
		Version  int            `json:"version"`
		Sources  map[string]any `json:"sources"`
		Concepts map[string]any `json:"concepts"`
	}
	data, err := json.Marshal(New())
	if err != nil {
		t.Fatal(err)
	}
	var old oldManifest
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("old-shape unmarshal of new manifest must not fail: %v", err)
	}
	if old.Version != 2 {
		t.Error("old readers still see version")
	}
}

// TestAdoptStampsFormat: adoption (the WithUpgrade path in pkg/engine)
// stamps the current format onto a pre-format manifest.
func TestAdoptStampsFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")
	raw, err := os.ReadFile("testdata/v02x.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsPreFormat() {
		t.Fatal("fixture must be pre-format")
	}
	m.FormatVersion = CurrentFormatVersion
	m.Engine = EngineVersion
	if err := m.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	m2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m2.IsPreFormat() || m2.FormatVersion != CurrentFormatVersion {
		t.Error("adopted manifest must carry the current format")
	}
	if len(m2.Sources) != 1 {
		t.Error("adoption must preserve existing sources")
	}
}
