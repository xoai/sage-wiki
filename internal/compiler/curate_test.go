package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// curateTestServer returns an httptest server serving one openai-shaped
// curation response per call, plus a mutex-guarded capture of the rendered
// prompts (httptest serves per-request goroutines — the #106/#143 race
// lesson).
func curateTestServer(t *testing.T, actions []map[string]any) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var captured []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		mu.Lock()
		captured = append(captured, string(body))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"content": mustJSONString(actions)},
			}},
			"model": "m",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	t.Cleanup(server.Close)
	return server, &captured
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func curateClient(t *testing.T, url string) *llm.Client {
	t.Helper()
	c, err := llm.NewClient("openai", "fake-key", url, -1)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCurateConcepts_FoldIntoExisting(t *testing.T) {
	server, captured := curateTestServer(t, []map[string]any{
		{"name": "rem-action-plan", "action": "fold", "into": "remedial-action-plan", "alias": "rem-action-plan"},
		{"name": "mw-3", "action": "keep"},
	})
	client := curateClient(t, server.URL)

	mf := manifest.New()
	mf.AddConcept("remedial-action-plan", "wiki/concepts/remedial-action-plan.md", []string{"raw/a.md"})

	new := []ExtractedConcept{
		{Name: "rem-action-plan", Type: "concept", Sources: []string{"raw/b.md"}, Aliases: []string{"RAP"}},
		{Name: "mw-3", Type: "concept", Sources: []string{"raw/c.md"}},
	}
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, sortedConceptNames(mf.Concepts), false, 200, mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Name != "mw-3" {
		t.Errorf("kept = %v, want [mw-3] (rem-action-plan folded)", kept)
	}
	con, ok := mf.Concepts["remedial-action-plan"]
	if !ok || !containsStr(con.Sources, "raw/b.md") {
		t.Errorf("fold must union sources into the manifest target: %+v", con)
	}
	if !ok || !containsStr(con.Aliases, "rem-action-plan") {
		t.Errorf("fold must record the alias: %+v", con)
	}
	if len(*captured) != 1 {
		t.Errorf("one chunk expected, got %d calls", len(*captured))
	}
	if !strings.Contains((*captured)[0], "CURATE-TEMPLATE-MARKER") && !strings.Contains((*captured)[0], "remedial-action-plan") {
		t.Errorf("prompt must carry the existing-concepts list")
	}
}

func TestCurateConcepts_FoldVetoedByNeverMerge(t *testing.T) {
	// The model proposes folding mw-3 into mw-2 — the #164 guard must veto it
	// regardless of the model's confidence.
	server, _ := curateTestServer(t, []map[string]any{
		{"name": "mw-3", "action": "fold", "into": "mw-2", "alias": "mw-3"},
	})
	client := curateClient(t, server.URL)

	mf := manifest.New()
	mf.AddConcept("mw-2", "wiki/concepts/mw-2.md", []string{"raw/a.md"})

	new := []ExtractedConcept{{Name: "mw-3", Type: "concept", Sources: []string{"raw/c.md"}}}
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, sortedConceptNames(mf.Concepts), false, 200, mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Name != "mw-3" {
		t.Errorf("vetoed fold must keep the concept, got %v", kept)
	}
}

func TestCurateConcepts_DropGated(t *testing.T) {
	dropAction := []map[string]any{{"name": "appendix-f", "action": "drop", "reason": "appendix label"}}

	// allow_drop=false (default): drop becomes a logged proposal, concept kept.
	server, _ := curateTestServer(t, dropAction)
	client := curateClient(t, server.URL)
	mf := manifest.New()
	new := []ExtractedConcept{{Name: "appendix-f", Type: "concept", Sources: []string{"raw/a.md"}}}
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, nil, false, 200, mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 {
		t.Errorf("drop without allow_drop must keep the concept, got %v", kept)
	}

	// allow_drop=true: drop applies.
	server2, _ := curateTestServer(t, dropAction)
	client2 := curateClient(t, server2.URL)
	mf2 := manifest.New()
	kept2, err := curateConcepts(context.Background(), client2, "m", prompts.NewRegistry(), new, nil, true, 200, mf2)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept2) != 0 {
		t.Errorf("drop with allow_drop must remove the concept, got %v", kept2)
	}
}

func TestCurateConcepts_ChunkingSeesEarlierKeeps(t *testing.T) {
	var calls struct {
		mu    sync.Mutex
		count int
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.mu.Lock()
		calls.count++
		n := calls.count
		calls.mu.Unlock()
		var arr []map[string]any
		if n == 1 {
			arr = []map[string]any{{"name": "alpha", "action": "keep"}}
		} else {
			arr = []map[string]any{{"name": "beta", "action": "keep"}}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": mustJSONString(arr)}}},
			"model":   "m",
		})
	}))
	t.Cleanup(server.Close)
	client := curateClient(t, server.URL)

	var new []ExtractedConcept
	for _, n := range []string{"alpha", "beta"} {
		new = append(new, ExtractedConcept{Name: n, Type: "concept", Sources: []string{"raw/x.md"}})
	}
	// batch_size=1 forces two chunks; both survive.
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, nil, false, 1, manifest.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Errorf("kept = %d, want 2", len(kept))
	}
	if calls.count != 2 {
		t.Errorf("chunked calls = %d, want 2", calls.count)
	}
}

func TestCurateConcepts_TotalFailureErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(server.Close)
	client := curateClient(t, server.URL)

	new := []ExtractedConcept{{Name: "alpha", Type: "concept", Sources: []string{"raw/x.md"}}}
	_, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, nil, false, 200, manifest.New())
	if err == nil {
		t.Fatal("total curation failure must return an error (callers count it and proceed uncurated)")
	}
}

func TestCurateConcepts_FoldNewIntoNewCanonicalLonger(t *testing.T) {
	// fold into another PROPOSED (not existing) concept: canonical = longer name.
	server, _ := curateTestServer(t, []map[string]any{
		{"name": "clean-fill-cover", "action": "fold", "into": "clean-soil-cover", "alias": "clean-fill-cover"},
		{"name": "clean-soil-cover", "action": "keep"},
	})
	client := curateClient(t, server.URL)
	mf := manifest.New()
	new := []ExtractedConcept{
		{Name: "clean-fill-cover", Type: "concept", Sources: []string{"raw/a.md"}},
		{Name: "clean-soil-cover", Type: "concept", Sources: []string{"raw/b.md"}},
	}
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, nil, false, 200, mf)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Name != "clean-soil-cover" {
		t.Fatalf("kept = %v, want [clean-soil-cover]", kept)
	}
	if !containsStr(kept[0].Sources, "raw/a.md") {
		t.Errorf("in-set fold must union sources: %v", kept[0].Sources)
	}
	if !containsStr(kept[0].Aliases, "clean-fill-cover") {
		t.Errorf("in-set fold must record alias: %v", kept[0].Aliases)
	}
}

func TestCurateConcepts_UnknownActionKeeps(t *testing.T) {
	// Application-layer tolerance: actions naming concepts NOT in the
	// proposed set are no-ops, and a fold whose target resolves nowhere
	// keeps the concept. (Enum-invalid actions are rejected earlier by the
	// schema — a hard call failure, covered by TotalFailureErrors.)
	server, _ := curateTestServer(t, []map[string]any{
		{"name": "not-proposed-at-all", "action": "drop", "reason": "x"},
		{"name": "beta", "action": "fold", "into": "does-not-exist-anywhere", "alias": "beta"},
	})
	client := curateClient(t, server.URL)
	new := []ExtractedConcept{
		{Name: "alpha", Type: "concept", Sources: []string{"raw/a.md"}},
		{Name: "beta", Type: "concept", Sources: []string{"raw/b.md"}},
	}
	kept, err := curateConcepts(context.Background(), client, "m", prompts.NewRegistry(), new, nil, true, 200, manifest.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Errorf("unknown action and dangling fold target must keep concepts, got %v", kept)
	}
}

var _ = errors.New // placeholder to keep imports stable
var _ = fmt.Sprintf
