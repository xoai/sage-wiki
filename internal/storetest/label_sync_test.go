package storetest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLabelInventorySync statically enforces the D3 cardinality inventory:
// every metrics.*Named call site in hook packages uses only label keys from
// the pinned set. (Runtime validation is a hot-path no-no per spec §6; the
// convention is enforced here.)
func TestLabelInventorySync(t *testing.T) {
	root := repoRoot(t)
	hookPkgs := []string{"internal/llm", "internal/embed", "internal/vectors",
		"internal/compiler", "internal/hybrid", "internal/query"}
	allowedKeys := map[string]bool{"pass": true, "stage": true, "direction": true, "provider": true, "cache": true}
	for _, pkg := range hookPkgs {
		dir := filepath.Join(root, pkg)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			// find all "key", "value" label pairs in Named calls
			for _, m := range regexp.MustCompile(`Named\("[a-z_]+",((?:\s*"[^"]*",?\s*)*)\)`).FindAllStringSubmatch(string(data), -1) {
				parts := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1)
				for i := 0; i+1 < len(parts); i += 2 {
					key := parts[i][1]
					if !allowedKeys[key] {
						t.Errorf("%s/%s: label key %q outside D3 inventory in %s", pkg, e.Name(), key, m[0])
					}
				}
			}
		}
	}
}

// TestRateLimitedSitesPinned greps the enumerated 429 discrimination sites
// (spec §2): the counter must appear in each transport file.
func TestRateLimitedSitesPinned(t *testing.T) {
	root := repoRoot(t)
	sites := map[string]string{
		"internal/llm/client.go":  "llm_rate_limited_total",
		"internal/llm/cache.go":   "llm_rate_limited_total",
		"internal/llm/stream.go":  "llm_rate_limited_total",
		"internal/llm/batch.go":   "recordRateLimited",
		"internal/llm/gemini.go":  "recordRateLimited",
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
