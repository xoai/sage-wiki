package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommunitiesDefaults(t *testing.T) {
	c := CommunitiesConfig{}
	if c.Enabled {
		t.Error("Enabled must default to false (cost gate)")
	}
	if got := c.MaxTokensOrDefault(); got != 1024 {
		t.Errorf("MaxTokensOrDefault = %d, want 1024", got)
	}
	if got := c.MaxCommunitiesOrDefault(); got != 8 {
		t.Errorf("MaxCommunitiesOrDefault = %d, want 8", got)
	}
	if got := c.MinMembersOrDefault(); got != 3 {
		t.Errorf("MinMembersOrDefault = %d, want 3", got)
	}
	over := CommunitiesConfig{MaxTokens: 512, MaxCommunities: 4, MinMembers: 5}
	if over.MaxTokensOrDefault() != 512 || over.MaxCommunitiesOrDefault() != 4 || over.MinMembersOrDefault() != 5 {
		t.Error("overrides not honored")
	}
	neg := CommunitiesConfig{MaxTokens: -1}
	if neg.MaxTokensOrDefault() != 1024 {
		t.Error("out-of-range must fall back, not clamp")
	}
}

func TestCommunitiesYamlParse(t *testing.T) {
	yamlDoc := []byte(`project: test
output: wiki
sources:
  - path: raw
ontology:
  communities:
    enabled: true
    model: cheap-model
    max_tokens: 512
    max_communities: 4
    min_members: 5
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
	c := cfg.Ontology.Communities
	if !c.Enabled || c.Model != "cheap-model" || c.MaxTokens != 512 || c.MaxCommunities != 4 || c.MinMembers != 5 {
		t.Errorf("parsed = %+v", c)
	}
}
