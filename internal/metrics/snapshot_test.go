package metrics

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/log"
)

// TestLogSnapshotEmitsAndGuards pins the emission contract (spec §3/§7.5):
// content in the line when series exist; NO line when the registry is empty.
func TestLogSnapshotEmitsAndGuards(t *testing.T) {
	var buf bytes.Buffer
	restore := log.SetLoggerForTest(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(restore)

	resetRegistry()
	LogSnapshot()
	if buf.Len() != 0 {
		t.Errorf("empty registry emitted a snapshot line: %q", buf.String())
	}

	CounterNamed("test_snap_emit_total").Add(3)
	LogSnapshot()
	out := buf.String()
	if !strings.Contains(out, "metrics snapshot") {
		t.Errorf("snapshot line missing: %q", out)
	}
	if !strings.Contains(out, "test_snap_emit_total=3") {
		t.Errorf("series missing from snapshot line: %q", out)
	}
}
