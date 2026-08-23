package compiler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
)

// qualityRetryHarness compiles one source whose article is BAD on the first
// write (too short to score) and GOOD on the retry, with the write prompts
// captured. The mock distinguishes passes by request content: the curation/
// extract branches are inert here (embedding strategy), summarization returns
// a long summary, and article writes alternate bad→good per concept.
func qualityRetryHarness(t *testing.T, retryOn bool, goodOnRetry bool) (*CompileResult, []string) {
	t.Helper()
	var mu sync.Mutex
	var writePrompts []string
	writeCalls := map[string]int{}

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
		case strings.Contains(lastMsg, "wiki author writing a comprehensive article"):
			mu.Lock()
			writePrompts = append(writePrompts, lastMsg)
			n := writeCalls["article"]
			writeCalls["article"] = n + 1
			mu.Unlock()
			if n == 0 || !goodOnRetry {
				// All-filler, no substance: grounding/coverage/antipattern floor
				// to 0 — combined far below the 0.5 threshold.
				content = "In conclusion, it is important to note that this article will delve into the topic. In summary, needless to say, at the end of the day, when it comes to remediation, in today's world. It is worth noting that this article discusses many things. Last but not least, in this article we delve into everything."
			} else {
				content = qualityGoodArticle("barium-mitigation")
			}
		case strings.Contains(lastMsg, "concept extraction system"):
			content = `[{"name":"barium-mitigation","aliases":[],"sources":["raw/site.md"],"type":"concept"}]`
		default:
			content = "## Key claims\n\n" + strings.Repeat("The site remediation record documents barium concentrations, mitigation measures, regulatory determinations, and sampling locations across every monitoring well. ", 12)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 100},
		})
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "quality-retry", "gemini-2.5-flash")
	cfg := `
version: 1
project: quality-retry
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
  quality:
    threshold: 0.5
`
	if retryOn {
		cfg += "    retry: true\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "site.md"),
		[]byte("# Site Remediation\n\n"+strings.Repeat("Barium concentrations in stockpile soils were mitigated under the recorded land use covenant; monitoring wells and laboratory analyses document every determination. ", 20)), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Compile(dir, CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return result, writePrompts
}

func qualityGoodArticle(concept string) string {
	// Long, structured, wikilinked, on-topic prose that clears the 0.5 bar.
	body := strings.Repeat(fmt.Sprintf("## %s Overview\n\nThe recorded site remediation program addresses barium-containing soils across the stockpile area, with laboratory confirmations and regulatory determinations documented for each monitoring well. See also [[%s]] for related context on sampling protocols.\n\n", concept, concept), 8)
	return body
}

// Issue #144 increment: with quality.retry on, a sub-threshold article gets
// exactly ONE fresh rewrite; still-low-after-retry is a counted error.
func TestQualityRetry_BlocksAndRetriesOnce(t *testing.T) {
	result, prompts := qualityRetryHarness(t, true, true)
	if len(prompts) != 2 {
		t.Fatalf("write calls = %d, want exactly 2 (original + one retry)", len(prompts))
	}
	// The retry must be a FRESH write: prepareArticle seeds ExistingArticle
	// from the on-disk file — the retry deleted it, so the second prompt must
	// NOT carry the bad article's body.
	if strings.Contains(prompts[1], "In conclusion, it is important to note") {
		t.Errorf("retry prompt seeded from the bad article — the on-disk file was not removed first (#145 prepareArticle gotcha)")
	}
	if result.Errors != 0 {
		t.Errorf("Errors = %d, want 0 (good-after-retry is a clean recovery, no error)", result.Errors)
	}
	if result.ArticlesWritten != 1 {
		t.Errorf("ArticlesWritten = %d, want 1", result.ArticlesWritten)
	}
}

// The blocking half: still-low after the ONE retry counts as a compile
// error — and there is never a second retry.
func TestQualityRetry_StillLowCountsError(t *testing.T) {
	result, prompts := qualityRetryHarness(t, true, false)
	if len(prompts) != 2 {
		t.Fatalf("write calls = %d, want exactly 2 (one retry, never more)", len(prompts))
	}
	if result.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (still-low-after-retry is a counted error)", result.Errors)
	}
}

// Default (retry off): today's advisory behavior — one write, no retry,
// no error counted for low quality.
func TestQualityRetry_DefaultAdvisoryUnchanged(t *testing.T) {
	result, prompts := qualityRetryHarness(t, false, true)
	if len(prompts) != 1 {
		t.Fatalf("write calls = %d, want 1 (advisory: no retry)", len(prompts))
	}
	if result.Errors != 0 {
		t.Errorf("Errors = %d, want 0 (advisory quality does not count errors)", result.Errors)
	}
}
