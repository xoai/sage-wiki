package llm

import (
	"os"
	"strings"
	"testing"
)

// TestStructuredPathNeverBuildsCachedRequest pins design D7 statically:
// structured.go must not reference the cached request path at all.
func TestStructuredPathNeverBuildsCachedRequest(t *testing.T) {
	data, err := os.ReadFile("structured.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"FormatCachedRequest", "cacheID", "ChatCompletionCached"} {
		if strings.Contains(string(data), needle) {
			t.Errorf("structured.go references %q — the structured path must use the direct transport only (D7)", needle)
		}
	}
}
