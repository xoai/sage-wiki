package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/events"
)

// setSkipSSRF enables the wiki SSRF bypass for httptest servers.
func setSkipSSRF(t *testing.T) func() {
	t.Helper()
	wiki.SkipSSRFCheck = true
	return func() { wiki.SkipSSRFCheck = false }
}

// SPEC-08 Task 7: engine Capture hardening — limits on the Reader path,
// slug sanitization + containment, UTF-8 gate (text surface), and
// limit_exceeded emission for ALL THREE modes (D2 event ownership).

func workspaceWithLimits(t *testing.T, limitsYAML string, sink events.Sink) *Workspace {
	t.Helper()
	dir := initWorkspace(t)
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
	w, err := Open(context.Background(), dir, WithEventSink(sink))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func findEvents(sink *captureSink, ty events.Type) []events.Event {
	var out []events.Event
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, ev := range sink.events {
		if ev.Type == ty {
			out = append(out, ev)
		}
	}
	return out
}

func TestCaptureReaderOversized(t *testing.T) {
	sink := &captureSink{}
	w := workspaceWithLimits(t, "limits:\n  max_doc_bytes: 100\n", sink)
	_, err := w.Capture(context.Background(), Source{
		Reader: strings.NewReader(strings.Repeat("a", 200)),
	})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if !errors.Is(err, ErrDocTooLarge) {
		t.Error("back-compat: errors.Is(err, ErrDocTooLarge) = false")
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Fatalf("limit_exceeded events = %d, want 1", len(got))
	}
	entries, _ := os.ReadDir(filepath.Join(w.Dir(), "raw", "captures"))
	if len(entries) != 0 {
		t.Errorf("oversized capture persisted %d files", len(entries))
	}
}

func TestCaptureReaderUTF16Rejected(t *testing.T) {
	sink := &captureSink{}
	w := workspaceWithLimits(t, "", sink)
	data := append([]byte{0xFF, 0xFE}, []byte("h\x00i\x00")...)
	_, err := w.Capture(context.Background(), Source{Reader: strings.NewReader(string(data))})
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("err = %v, want ErrEncoding", err)
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Errorf("limit_exceeded events = %d, want 1", len(got))
	}
	entries, _ := os.ReadDir(filepath.Join(w.Dir(), "raw", "captures"))
	if len(entries) != 0 {
		t.Errorf("rejected capture persisted %d files", len(entries))
	}
}

func TestCaptureReaderInvalidUTF8Rejected(t *testing.T) {
	sink := &captureSink{}
	w := workspaceWithLimits(t, "", sink)
	_, err := w.Capture(context.Background(), Source{
		Reader: strings.NewReader(string([]byte{0xFF, 0xC3, 0x28})),
	})
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("err = %v, want ErrEncoding (capture is a text surface)", err)
	}
}

func TestCaptureReaderUTF8BOMAccepted(t *testing.T) {
	w := workspaceWithLimits(t, "", nil)
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello")...)
	id, err := w.Capture(context.Background(), Source{Reader: strings.NewReader(string(data))})
	if err != nil {
		t.Fatalf("UTF-8 with BOM must be accepted: %v", err)
	}
	if string(id) == "" {
		t.Error("empty DocID")
	}
}

func TestCaptureTypeTraversalSanitized(t *testing.T) {
	w := workspaceWithLimits(t, "", nil)
	id, err := w.Capture(context.Background(), Source{
		Reader: strings.NewReader("content"),
		Type:   "../../evil",
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	rel := filepath.ToSlash(string(id))
	if strings.Contains(rel, "..") || !strings.HasPrefix(rel, "raw/captures/") {
		t.Fatalf("capture escaped raw/captures: %q", rel)
	}
	if _, err := os.Stat(filepath.Join(w.Dir(), rel)); err != nil {
		t.Errorf("captured file missing: %v", err)
	}
}

func TestCaptureTypeEmptyAfterSanitizeRejected(t *testing.T) {
	sink := &captureSink{}
	w := workspaceWithLimits(t, "", sink)
	_, err := w.Capture(context.Background(), Source{
		Reader: strings.NewReader("content"),
		Type:   "///",
	})
	if err == nil {
		t.Fatal("Type that sanitizes to empty must be rejected")
	}
	entries, _ := os.ReadDir(filepath.Join(w.Dir(), "raw", "captures"))
	if len(entries) != 0 {
		t.Errorf("rejected capture persisted %d files", len(entries))
	}
}

func TestCapturePathModeEmitsLimitEvent(t *testing.T) {
	sink := &captureSink{}
	w := workspaceWithLimits(t, "limits:\n  max_doc_bytes: 100\n", sink)
	src := filepath.Join(t.TempDir(), "big.md")
	if err := os.WriteFile(src, []byte(strings.Repeat("b", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := w.Capture(context.Background(), Source{Path: src})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Fatalf("limit_exceeded events = %d, want 1 (engine owns emission for Path mode)", len(got))
	}
}

func TestCaptureURLModeEmitsLimitEvent(t *testing.T) {
	SkipSSRF := setSkipSSRF(t)
	defer SkipSSRF()
	sink := &captureSink{}
	w := workspaceWithLimits(t, "limits:\n  max_doc_bytes: 100\n", sink)
	server := httptest.NewServer(http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
		wr.Write([]byte(strings.Repeat("c", 200)))
	}))
	defer server.Close()
	_, err := w.Capture(context.Background(), Source{URL: server.URL + "/big"})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *limits.LimitError", err)
	}
	if got := findEvents(sink, events.TypeLimitExceeded); len(got) != 1 {
		t.Fatalf("limit_exceeded events = %d, want 1 (engine owns emission for URL mode)", len(got))
	}
}
