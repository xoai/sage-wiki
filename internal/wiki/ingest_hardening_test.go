package wiki

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/manifest"
)

// SPEC-08 Task 6: size caps, overflow detection, UTF-8 gate with binary
// routing, BOM precedence. All violations return typed errors upward —
// internal/wiki emits nothing (the engine Capture wrapper owns events).

func greenfieldWithLimits(t *testing.T, limitsYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if err := InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	if limitsYAML != "" {
		cfgPath := filepath.Join(dir, "config.yaml")
		old, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, append(old, []byte(limitsYAML)...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func sourceCount(t *testing.T, dir string) int {
	t.Helper()
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return mf.SourceCount()
}

func TestIngestPathOversizedRejected(t *testing.T) {
	dir := greenfieldWithLimits(t, "limits:\n  max_doc_bytes: 100\n")
	src := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(src, []byte(strings.Repeat("x", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := IngestPath(dir, src)
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if !errors.Is(err, limits.ErrDocTooLarge) {
		t.Errorf("errors.Is(err, ErrDocTooLarge) = false")
	}
	if sourceCount(t, dir) != 0 {
		t.Error("oversized ingest must not register a source")
	}
}

func TestIngestPathBinaryRoutedWithExtension(t *testing.T) {
	dir := greenfieldWithLimits(t, "")
	src := filepath.Join(t.TempDir(), "doc.pdf")
	// %PDF magic + invalid-UTF-8 binary junk.
	data := append([]byte("%PDF-1.4\n"), 0xFF, 0xFE, 0x00, 0x81)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := IngestPath(dir, src)
	if err != nil {
		t.Fatalf("binary with known extractor extension must route, got: %v", err)
	}
	if !strings.HasSuffix(res.SourcePath, ".pdf") {
		t.Errorf("routed binary lost its extension: %s", res.SourcePath)
	}
	if _, err := os.Stat(filepath.Join(dir, res.SourcePath)); err != nil {
		t.Errorf("routed binary not copied: %v", err)
	}
}

func TestIngestPathInvalidUTF8Rejected(t *testing.T) {
	dir := greenfieldWithLimits(t, "")
	src := filepath.Join(t.TempDir(), "junk.md")
	if err := os.WriteFile(src, []byte{0xFF, 0xFE, 0xC3, 0x28}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := IngestPath(dir, src)
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("err = %v, want ErrEncoding", err)
	}
	if sourceCount(t, dir) != 0 {
		t.Error("rejected content must not register a source")
	}
}

func TestIngestPathUTF16BOMWithPDFExtensionRejected(t *testing.T) {
	// BOM detection PRECEDES the extractor branch: UTF-16 content is
	// rejected even when the extension matches a binary extractor.
	dir := greenfieldWithLimits(t, "")
	src := filepath.Join(t.TempDir(), "trap.pdf")
	data := append([]byte{0xFF, 0xFE}, []byte("h\x00e\x00l\x00l\x00o\x00")...)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := IngestPath(dir, src)
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("err = %v, want ErrEncoding (BOM precedence)", err)
	}
}

func TestIngestPathUTF8BOMAccepted(t *testing.T) {
	dir := greenfieldWithLimits(t, "")
	src := filepath.Join(t.TempDir(), "bom.md")
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("# Title\ncontent")...)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := IngestPath(dir, src); err != nil {
		t.Fatalf("UTF-8 with BOM must be accepted: %v", err)
	}
}

func TestIngestURLOversizedErrorsNotTruncates(t *testing.T) {
	dir := greenfieldWithLimits(t, "limits:\n  max_doc_bytes: 100\n")
	SkipSSRFCheck = true
	defer func() { SkipSSRFCheck = false }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("y", 200)))
	}))
	defer server.Close()

	_, err := IngestURL(dir, server.URL+"/big")
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError (no silent truncation)", err)
	}
	if sourceCount(t, dir) != 0 {
		t.Error("oversized download must not persist")
	}
}

func TestIngestURLInvalidUTF8Rejected(t *testing.T) {
	dir := greenfieldWithLimits(t, "")
	SkipSSRFCheck = true
	defer func() { SkipSSRFCheck = false }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte{0xFF, 0xFE, 0xC3, 0x28})
	}))
	defer server.Close()

	_, err := IngestURL(dir, server.URL+"/junk")
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("err = %v, want ErrEncoding", err)
	}
	if sourceCount(t, dir) != 0 {
		t.Error("rejected download must not persist")
	}
}
