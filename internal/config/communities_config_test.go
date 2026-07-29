package config

import "testing"

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
