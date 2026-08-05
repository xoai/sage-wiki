package pathological

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// rssCeilingBytes is the AC4 peak-RSS ceiling (512 MiB).
const rssCeilingBytes int64 = 512 << 20

// TestPathologicalLimits drives engine capture + tier-0 compile over the
// SPEC-08 D8 pathological corpus and asserts every limit holds with a typed
// error, nothing partial persists, and peak RSS stays under the ceiling.
// Fully offline: tier 0 is index-only (no LLM, no embedder). Skipped under
// -short.
func TestPathologicalLimits(t *testing.T) {
	if testing.Short() {
		t.Skip("pathological corpus skipped under -short")
	}
	ctx := context.Background()

	wsDir := t.TempDir()
	if err := wiki.InitGreenfield(wsDir, "pathological", "gpt-4o-mini"); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	w, err := engine.Open(ctx, wsDir)
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	defer w.Close()

	// The on-disk batch docs are generated DIRECTLY into raw/ (not via
	// capture): capture registers a source in the manifest, which the compile
	// diff then reads as unchanged — the batch ceiling counts diff Added +
	// Modified, so the realistic over-batch scenario is many files dropped
	// into raw/ that have never been ingested.
	corpus, err := Generate(filepath.Join(wsDir, "raw"))
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}

	// (1) Oversized capture (streaming ~1 GiB) fails typed with no file
	// persisted — the size gate reads at most max_doc_bytes+1.
	_, err = w.Capture(ctx, engine.Source{Reader: corpus.OversizedReader()})
	var le *limits.LimitError
	if !errors.As(err, &le) {
		t.Fatalf("oversized capture error = %v, want *limits.LimitError", err)
	}
	if le.Which != limits.WhichDocBytes {
		t.Errorf("oversized Which = %q, want %q", le.Which, limits.WhichDocBytes)
	}
	assertNoCaptureFile(t, wsDir)

	// (2) Binary junk capture fails with the typed encoding error, no file.
	_, err = w.Capture(ctx, engine.Source{Reader: strings.NewReader(string(BinaryJunk()))})
	if !errors.Is(err, limits.ErrEncoding) {
		t.Fatalf("binary capture error = %v, want limits.ErrEncoding", err)
	}
	assertNoCaptureFile(t, wsDir)

	// (3) Tier-0 compile over the over-batch raw/ set fails fast with the
	// typed batch error before any doc is processed.
	res, err := w.Compile(ctx, engine.CompileRequest{Selector: "pending", Tier: 0})
	if !errors.As(err, &le) {
		t.Fatalf("compile error = %v, want *limits.LimitError", err)
	}
	if le.Which != limits.WhichCompileBatch {
		t.Errorf("compile Which = %q, want %q", le.Which, limits.WhichCompileBatch)
	}
	if res != nil && (res.Added != 0 || res.Summarized != 0 || res.ArticlesWritten != 0) {
		t.Errorf("over-batch compile processed docs before failing: %+v", res)
	}

	// (5) Peak RSS stays under the ceiling. VmHWM on Linux; behavior-only
	// elsewhere (the typed-limit assertions above already ran).
	if runtime.GOOS == "linux" {
		peak := peakRSSBytes(t)
		if peak > rssCeilingBytes {
			t.Errorf("peak RSS = %d bytes, exceeds ceiling %d", peak, rssCeilingBytes)
		} else {
			t.Logf("peak RSS = %d bytes (ceiling %d)", peak, rssCeilingBytes)
		}
	}
}

// assertNoCaptureFile asserts no reader-capture file landed in raw/captures.
func assertNoCaptureFile(t *testing.T, wsDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(wsDir, "raw", "captures"))
	if err != nil {
		if os.IsNotExist(err) {
			return // directory never created — nothing persisted
		}
		t.Fatalf("read captures dir: %v", err)
	}
	if len(entries) > 0 {
		t.Errorf("expected no capture file persisted, found %d entries", len(entries))
	}
}

// peakRSSBytes reads VmHWM (peak resident set size) from /proc/self/status.
func peakRSSBytes(t *testing.T) int64 {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatalf("read /proc/self/status: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("malformed VmHWM line: %q", line)
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatalf("parse VmHWM %q: %v", fields[1], err)
		}
		return kb * 1024
	}
	t.Fatal("VmHWM not found in /proc/self/status")
	return 0
}
