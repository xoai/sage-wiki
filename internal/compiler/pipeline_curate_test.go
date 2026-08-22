package compiler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// Issue #167, integration: with dedup_strategy: "llm" the full compile path
// must invoke the curation pass between extraction and article writing —
// the fold applies (sources unioned into the manifest target + alias
// recorded), the drop is gated by allow_drop, and the curation prompt is
// actually rendered from the curate_concepts template. Sentinel discipline
// (#143 lesson): the curation branch is keyed on the TEMPLATE's own phrase
// ("concept curation system"), which appears nowhere in source content.
func TestCompile_LLMCurationFoldsAndGatesDrops(t *testing.T) {
	var mu sync.Mutex
	var curatePrompts []string
	var articleNames []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		lastMsg := ""
		if len(messages) > 0 {
			if m, ok := messages[len(messages)-1].(map[string]any); ok {
				lastMsg, _ = m["content"].(string)
			}
		}

		var content string
		switch {
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name":"clean-fill-cover","aliases":[],"sources":["raw/article1.md"],"type":"concept"},` +
				`{"name":"clean-soil-cover","aliases":[],"sources":["raw/article2.md"],"type":"concept"},` +
				`{"name":"appendix-f","aliases":[],"sources":["raw/article1.md"],"type":"concept"}]`
		case strings.Contains(lastMsg, "concept curation system"):
			// The curation call: fold the restatement into its sibling
			// (in-set, canonical = longer name), drop the appendix label.
			mu.Lock()
			curatePrompts = append(curatePrompts, lastMsg)
			mu.Unlock()
			content = `[{"name":"clean-fill-cover","action":"fold","into":"clean-soil-cover","alias":"clean-fill-cover"},` +
				`{"name":"clean-soil-cover","action":"keep"},` +
				`{"name":"appendix-f","action":"drop","reason":"appendix label, not a concept"}]`
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			mu.Lock()
			articleNames = append(articleNames, lastMsg)
			mu.Unlock()
			content = "# Article\n\n## Definition\n\nA defined concept with sufficient prose to pass validation.\n\n## See also\n"
		default:
			content = "## Key claims\n\nThis document discusses a remedial action plan, monitoring well mw-3, " +
				"and references appendix F, giving every concept a citing source for the evidence gate.\n\n## Concepts\n\nrem-action-plan, mw-3, appendix-f"
		}

		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": content}},
			},
			"model": "gpt-4o-mini",
			"usage": map[string]int{"total_tokens": 100},
		})
	}))
	defer server.Close()

	runCompile := func(allowDrop bool) (*CompileResult, []string, []string) {
		dir := t.TempDir()
		wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
		cfg := `
version: 1
project: test
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + server.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
  dedup_strategy: llm
`
		if allowDrop {
			cfg += "  llm_dedup:\n    allow_drop: true\n"
		}
		os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644)
		os.WriteFile(filepath.Join(dir, "raw", "article1.md"),
			[]byte("# Clean Fill Cover\n\nThe clean fill cover and clean soil cover both cap the stockpile; see appendix F."), 0644)

		mu.Lock()
		curatePrompts = nil
		articleNames = nil
		mu.Unlock()

		os.WriteFile(filepath.Join(dir, "raw", "article2.md"),
			[]byte("# Clean Soil Cover\n\nThe clean soil cover caps the stockpile area."), 0644)

		result, err := Compile(dir, CompileOpts{})
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		return result, curatePrompts, articleNames
	}

	// allow_drop=false (default): fold applies, drop is a logged proposal.
	result, prompts, articles := runCompile(false)
	if len(prompts) == 0 {
		t.Fatal("curation call never fired — dedup_strategy wiring broken")
	}
	if !strings.Contains(prompts[0], "clean-fill-cover") {
		t.Errorf("curation prompt missing proposed concepts")
	}
	if result.ConceptsExtracted != 2 {
		t.Errorf("concepts = %d, want 2 (fold + kept; drop gated)", result.ConceptsExtracted)
	}
	if len(articles) != 2 {
		t.Errorf("articles written = %d, want 2", len(articles))
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("unreachable")
	}

	// allow_drop=true: the drop applies.
	result2, _, articles2 := runCompile(true)
	if result2.ConceptsExtracted != 1 {
		t.Errorf("concepts = %d, want 1 (fold + drop applied)", result2.ConceptsExtracted)
	}
	if len(articles2) != 1 {
		t.Errorf("articles written = %d, want 1", len(articles2))
	}
}
