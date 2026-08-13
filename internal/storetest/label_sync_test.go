package storetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRateLimitedSitesPinned greps the enumerated 429 discrimination sites
// (spec §2): the counter must appear in each transport file.
func TestRateLimitedSitesPinned(t *testing.T) {
	root := repoRoot(t)
	sites := map[string]string{
		"internal/llm/client.go": "llm_rate_limited_total",
		"internal/llm/cache.go":  "llm_rate_limited_total",
		"internal/llm/stream.go": "llm_rate_limited_total",
		"internal/llm/batch.go":  "recordRateLimited",
		"internal/llm/gemini.go": "recordRateLimited",
	}
	for file, needle := range sites {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if !strings.Contains(string(data), needle) {
			t.Errorf("%s missing 429 hook %q", file, needle)
		}
	}
}
