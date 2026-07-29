package query

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/store"
)

// P3-5 T8: GlobalQA — level selection, scoped selection, map-reduce,
// community citations.

func seedCommunities(t *testing.T, h *graphqaHarness) {
	t.Helper()
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		h.addEntity(t, id, id)
	}
	comms := []store.Community{
		{ID: "c0-0", Level: 0, MemberCount: 4, EdgeCount: 3, UpdatedAt: "2026-07-29T00:00:00Z"},
		{ID: "c0-1", Level: 0, MemberCount: 3, EdgeCount: 2, UpdatedAt: "2026-07-29T00:00:00Z"},
		{ID: "c1-0", Level: 1, MemberCount: 7, EdgeCount: 6, UpdatedAt: "2026-07-29T00:00:00Z"},
	}
	members := map[string][]string{
		"c0-0": {"a", "b", "c", "d"},
		"c0-1": {"e", "f", "g"},
		"c1-0": {"a", "b", "c", "d", "e", "f", "g"},
	}
	cs := store.CommunityStore(h.ont)
	if _, err := cs.ReplaceDetection(comms, members); err != nil {
		t.Fatal(err)
	}
	cs.SetSummary("c0-0", "attention mechanisms and their optimizations", "h0", "m")
	_ = cs
	cs.SetSummary("c0-1", "distributed training infrastructure", "h1", "m")
	cs.SetSummary("c1-0", "machine learning systems overall", "h2", "m")
	// Index summaries so the searcher can find them.
	h.mem.Add(store.Entry{ID: "community:c0-0", Content: "attention mechanisms and their optimizations"})
	h.mem.Add(store.Entry{ID: "community:c0-1", Content: "distributed training infrastructure"})
	h.mem.Add(store.Entry{ID: "community:c1-0", Content: "machine learning systems overall"})
}

func TestGlobalQAHappyPath(t *testing.T) {
	h := newGraphqaHarness(t)
	seedCommunities(t, h)
	srv, _, _ := graphqaServer(t, `{"answer":"global themes","cited":[1]}`)
	client := testClient(t, srv.URL)

	res, err := GlobalQA(context.Background(), store.CommunityStore(h.ont), h.searcher, client,
		"attention mechanisms and distributed training?", GlobalQAOpts{Model: "m", MaxTokens: 512, MinMembers: 3, MaxParallel: 2})
	if err != nil {
		t.Fatalf("GlobalQA: %v", err)
	}
	if res.Answer == "" {
		t.Error("empty answer")
	}
	// Level 1 holds ONE community (fails ">1 community"), level 0 holds two
	// summarized ones → the walk must pick level 0.
	if res.Level != 0 {
		t.Errorf("level = %d, want 0", res.Level)
	}
	if len(res.Cited) == 0 {
		t.Fatal("no community citations — Cited could regress to always-empty")
	}
	for _, c := range res.Cited {
		if c.ID == "" || c.MemberCount < 3 {
			t.Errorf("bad citation: %+v", c)
		}
	}
}

func TestGlobalQANoCommunities(t *testing.T) {
	h := newGraphqaHarness(t)
	srv, _, _ := graphqaServer(t, `{"answer":"x"}`)
	client := testClient(t, srv.URL)
	_, err := GlobalQA(context.Background(), store.CommunityStore(h.ont), h.searcher, client,
		"anything?", GlobalQAOpts{Model: "m"})
	if err != ErrNoCommunities {
		t.Errorf("err = %v, want ErrNoCommunities", err)
	}
}

func TestGlobalQALevelWalkSkipsUnsummarizedTop(t *testing.T) {
	h := newGraphqaHarness(t)
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		h.addEntity(t, id, id)
	}
	cs := store.CommunityStore(h.ont)
	comms := []store.Community{
		{ID: "c0-0", Level: 0, MemberCount: 4, UpdatedAt: "t"},
		{ID: "c1-0", Level: 1, MemberCount: 4, UpdatedAt: "t"},
		{ID: "c1-1", Level: 1, MemberCount: 3, UpdatedAt: "t"},
	}
	members := map[string][]string{
		"c0-0": {"a", "b", "c", "d"},
		"c1-0": {"a", "b", "c", "d"},
		"c1-1": {"e", "f", "g"},
	}
	if _, err := cs.ReplaceDetection(comms, members); err != nil {
		t.Fatal(err)
	}
	// Level 1 has >1 community but NO summaries → walk must pick level 0.
	cs.SetSummary("c0-0", "level zero theme", "h0", "m")
	_ = cs

	chosen, summarized := pickLevel(cs, 3)
	if chosen != 0 {
		t.Errorf("chosen = %d, want 0 (level 1 unsummarized)", chosen)
	}
	if len(summarized) != 1 || summarized[0].ID != "c0-0" {
		t.Errorf("summarized = %+v", summarized)
	}
}

func TestGlobalQAEmptyQuestion(t *testing.T) {
	h := newGraphqaHarness(t)
	_, err := GlobalQA(context.Background(), store.CommunityStore(h.ont), h.searcher, nil, "", GlobalQAOpts{})
	if err == nil || !strings.Contains(err.Error(), "question is required") {
		t.Errorf("empty question must error, got %v", err)
	}
}

// Empty-after-filter: search hits that are all non-community docs leave
// nothing to map — the gate sentinel, per spec.
func TestGlobalQAEmptyAfterFilterIsSentinel(t *testing.T) {
	h := newGraphqaHarness(t)
	seedCommunities(t, h)
	// Bury the community docs under non-community hits for this query.
	for i := 0; i < 30; i++ {
		h.mem.Add(store.Entry{ID: fmt.Sprintf("concept:filler-%d", i), Content: "completely unrelated zebra giraffe platypus unrelated"})
	}
	srv, _, _ := graphqaServer(t, `{"answer":"x"}`)
	client := testClient(t, srv.URL)
	_, err := GlobalQA(context.Background(), store.CommunityStore(h.ont), h.searcher, client,
		"zebra giraffe platypus?", GlobalQAOpts{Model: "m", MaxTokens: 64, MinMembers: 3})
	if err == nil || !strings.Contains(err.Error(), "no communities match") {
		t.Errorf("err = %v, want the search-starvation error", err)
	}
}
