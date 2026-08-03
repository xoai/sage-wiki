package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/export"
)

// stubLLM serves one completion shape for any request.
func stubLLM(t *testing.T, model string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "## Key claims\n\nThis document discusses the main concepts and findings related to the test subject matter.\n\n## Concepts\n\ntest-concept: A fundamental concept extracted from the source material."}, "finish_reason": "stop"}},
			"model":   model,
			"usage":   map[string]int{"prompt_tokens": 800, "completion_tokens": 200, "total_tokens": 1000},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func initWorkspaceWithConfig(t *testing.T, cfgExtra string) string {
	t.Helper()
	dir := initWorkspace(t)
	cfg, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), append(cfg, []byte(cfgExtra)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCaptureReaderAndPath(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	id, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("# Hello\n\nCaptured content.")})
	if err != nil {
		t.Fatalf("Capture reader: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, string(id))); err != nil {
		t.Errorf("captured file missing: %v", err)
	}

	src := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(src, []byte("# External doc\n\nBody text here."), 0o644); err != nil {
		t.Fatal(err)
	}
	id2, err := w.Capture(context.Background(), Source{Path: src})
	if err != nil {
		t.Fatalf("Capture path: %v", err)
	}
	if id2 == "" {
		t.Error("path capture must return a DocID")
	}

	if _, err := w.Capture(context.Background(), Source{}); err == nil {
		t.Error("empty source must error")
	}
}

func TestCaptureReadOnlyRejected(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir, WithReadOnly())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("x")}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("err = %v, want ErrReadOnly", err)
	}
}

func TestCompileAndMaxCostGuard(t *testing.T) {
	srv := stubLLM(t, "gpt-4o-mini")
	dir := initWorkspace(t)
	extra := `version: 1
project: ws
sources:
  - path: raw
    type: auto
    watch: true
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + srv.URL + `
models:
  summarize: gpt-4o-mini
  extract: gpt-4o-mini
  write: gpt-4o-mini
compiler:
  auto_commit: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "article.md"), []byte("# Self-Attention\n\nSelf-attention computes contextual representations over sequences."), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// MaxCost low enough to trip after the first paid pass.
	maxCost := decimal.NewFromFloat(0.000001)
	res, err := w.Compile(context.Background(), CompileRequest{Selector: "pending", Tier: 3, MaxCost: &maxCost})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("err = %v, want ErrBudgetExceeded", err)
	}
	if res == nil {
		t.Fatal("partial result must accompany ErrBudgetExceeded")
	}
	if res.Summarized == 0 {
		t.Error("the summarize pass completed before the guard tripped — partial result must show it")
	}

	// Bad selector and tier rejected before any work.
	if _, err := w.Compile(context.Background(), CompileRequest{Selector: "glob:**"}); err == nil {
		t.Error("unsupported selector must error")
	}
	if _, err := w.Compile(context.Background(), CompileRequest{Tier: 9}); err == nil {
		t.Error("out-of-range tier must error")
	}
}

func TestStatsAndExport(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	stats, err := w.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	_ = stats // greenfield stats are all zeros; the call must succeed

	var buf bytes.Buffer
	if err := w.Export(context.Background(), &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		names[hdr.Name] = true
	}
	for _, want := range []string{"config.yaml", ".manifest.json", ".sage/wiki.db"} {
		if !names[want] {
			t.Errorf("export missing %s (has %v)", want, names)
		}
	}
}

// TestCaptureReaderDedup: two Reader captures in the same second must not
// overwrite each other (F-050).
func TestCaptureReaderDedup(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	id1, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("first")})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := w.Capture(context.Background(), Source{Reader: strings.NewReader("second")})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatalf("same-second captures collided: %q", id1)
	}
	data, err := os.ReadFile(filepath.Join(dir, string(id1)))
	if err != nil || !strings.Contains(string(data), "first") {
		t.Errorf("first capture lost/corrupt: %q %v", data, err)
	}
	data2, _ := os.ReadFile(filepath.Join(dir, string(id2)))
	if !strings.Contains(string(data2), "second") {
		t.Errorf("second capture content = %q", data2)
	}
	if !strings.Contains(string(data), "source: capture\n") {
		t.Errorf("capture must carry the unified frontmatter, got:\n%s", data)
	}
}

// TestBatchMaxCostRejected: Batch+MaxCost errors honestly (F-051).
func TestBatchMaxCostRejected(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	mc := decimal.NewFromFloat(1)
	if _, err := w.Compile(context.Background(), CompileRequest{Batch: true, MaxCost: &mc}); err == nil {
		t.Error("Batch+MaxCost must be rejected")
	}
}

// TestExport_MatchesSharedExporter pins SPEC-04 D5: the engine's Export is
// byte-identical to the shared deterministic exporter over the same tree.
func TestExport_MatchesSharedExporter(t *testing.T) {
	dir := initWorkspace(t)
	w, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var engineBuf bytes.Buffer
	if err := w.Export(context.Background(), &engineBuf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	var sharedBuf bytes.Buffer
	if err := export.Tar(context.Background(), dir, &sharedBuf); err != nil {
		t.Fatalf("export.Tar: %v", err)
	}
	if !bytes.Equal(engineBuf.Bytes(), sharedBuf.Bytes()) {
		t.Fatal("engine.Export bytes differ from shared exporter bytes")
	}
}
