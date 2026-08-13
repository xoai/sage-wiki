// Package storetest hosts the storage conformance suite (P2-1 T10) and the
// escape-hatch lint test (T9): no consumer outside the store packages may
// touch raw *sql.DB handles or the transitional Unwrap bridge.
package storetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedRawHandleFiles may reference ReadDB()/WriteDB() — the store
// implementations and the Backend surface itself. Everything else must go
// through store interfaces. Paths are repo-root-relative prefixes.
var allowedRawHandleDirs = []string{
	"internal/storage/",
	"internal/sqlitestore/",
	"internal/store/",
	"internal/memory/",    // sqlite EntryStore/ChunkStore impl
	"internal/vectors/",   // sqlite VectorStore impl
	"internal/ontology/",  // sqlite OntologyStore impl
	"internal/trust/",     // sqlite TrustStore impl
	"internal/compiler/",  // sqlite CompileItemStore impl + pipeline (T9a-2 pending)
	"internal/linter/",    // learning fns are the sqlite LearningStore impl
	"internal/query/",     // query pipeline — Backend injection pending (T9a-2)
	"internal/wiki/",      // reconciler/status — Backend injection pending (T9a-2)
	"internal/app/",       // container: Unwrap bridge for concrete App fields (T5)
	"internal/storedial/", // the OpenConcrete transitional helper itself (T6)
}

// TestNoRawHandleEscapes greps all non-test Go files for raw handle calls
// outside the allowed implementation packages.
func TestNoRawHandleEscapes(t *testing.T) {
	root := repoRoot(t)
	var violations []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, dir := range allowedRawHandleDirs {
			if strings.HasPrefix(rel, dir) {
				return nil
			}
		}
		if strings.HasPrefix(rel, "sage/") || strings.HasPrefix(rel, "vendor/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, ".ReadDB()") || strings.Contains(line, ".WriteDB()") {
				violations = append(violations, rel+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
			if strings.Contains(line, "sqlitestore.Unwrap(") {
				violations = append(violations, rel+":"+itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, v := range violations {
		t.Errorf("escape hatch: %s", v)
	}
}

// TestMcpSseHasNoMetricsEndpoint pins the D8 topology decision: the MCP
// transport (stdio or SSE) is not an ops surface and must never register
// the metrics handler.
func TestMcpSseHasNoMetricsEndpoint(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "mcp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"/metrics"`) {
			t.Errorf("internal/mcp/%s registers a /metrics route — MCP is not an ops surface (design D8)", e.Name())
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func itoa(n int) string {
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
