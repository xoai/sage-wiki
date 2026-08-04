package parity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/pkg/events"
)

// eventCaptureSink collects the run's events for the golden sequence.
type eventCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *eventCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

// TestEventSequenceGolden (SPEC-07 AC-1): compiling the golden corpus in
// replay mode emits the documented event sequence — field-by-field against
// testdata/golden/events.jsonl with id/time/durations normalized and the
// JobID injected ("golden-job") so every field is reproducible.
func TestEventSequenceGolden(t *testing.T) {
	if os.Getenv("SAGE_PARITY_FORCE") == "1" {
		t.Skip("SAGE_PARITY_FORCE=1: regen tests own the build (parity TestMain contract)")
	}
	got := buildEventSequence(t)

	wantRaw, err := os.ReadFile(goldenPath("events.jsonl"))
	if err != nil {
		t.Fatalf("golden events.jsonl missing — regenerate with SAGE_PARITY_FORCE=1 go test ./internal/parity/ -run TestRegenEventGolden: %v", err)
	}
	want := strings.Split(strings.TrimSpace(string(wantRaw)), "\n")
	if len(got) != len(want) {
		t.Fatalf("event sequence length = %d, want %d (golden mismatch — if intentional, regenerate)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d differs:\n got  %s\n want %s", i, got[i], want[i])
		}
	}
}

// TestRegenEventGolden regenerates testdata/golden/events.jsonl under
// SAGE_PARITY_FORCE=1 (same guard as the artifact goldens).
func TestRegenEventGolden(t *testing.T) {
	if os.Getenv("SAGE_PARITY_FORCE") != "1" {
		t.Skip("set SAGE_PARITY_FORCE=1 to regenerate the event golden")
	}
	lines := buildEventSequence(t)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(goldenPath("events.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d events)", goldenPath("events.jsonl"), len(lines))
}

// buildEventSequence compiles the golden corpus through the replay server
// with an injected JobID + capture sink and returns the normalized event
// lines (one JSON object per line, deterministic).
func buildEventSequence(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata")
	replay, err := NewReplayServer(filepath.Join(root, "fixtures", "openai"))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	ws, err := os.MkdirTemp("", "parity-events-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(ws) })

	sink := &eventCaptureSink{}
	err = buildWorkspaceOpts(
		filepath.Join(root, "golden-corpus"), ws, replay.URL(),
		readGoldenConfigForMain(root), "sk-replay", "gpt-4o-mini",
		compiler.CompileOpts{JobID: "golden-job", Sink: sink},
	)
	if err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	lines := make([]string, 0, len(sink.events))
	for _, ev := range sink.events {
		lines = append(lines, normalizeEventLine(t, ev))
	}
	return canonicalEventOrder(lines)
}

// canonicalEventOrder makes the sequence reproducible: the stream ORDER of
// interior events is completion-order (concurrent LLM calls finish in
// nondeterministic order — inherent, and the artifacts stay deterministic
// because application is ordered, SPEC-04). The golden therefore pins the
// lifecycle brackets in place and sorts everything between them.
func canonicalEventOrder(lines []string) []string {
	key := func(line string) int {
		switch {
		case strings.Contains(line, `"type":"compile_started"`):
			return 0
		case strings.Contains(line, `"type":"compile_finished"`):
			return 2
		default:
			return 1
		}
	}
	out := append([]string{}, lines...)
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := key(out[i]), key(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i] < out[j]
	})
	return out
}

// normalizeEventLine renders one event with the non-reproducible fields
// sentinel-replaced: id, time, the workspace NAME (a temp dir in the
// harness), and duration fields (wall-clock by nature). JobID is NOT
// normalized — the harness injects "golden-job", so it compares verbatim
// (AC-1 determinism clause).
func normalizeEventLine(t *testing.T, ev events.Event) string {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "<id>"
	m["time"] = "<time>"
	m["workspace"] = "<ws>"
	if data, ok := m["data"].(map[string]any); ok {
		for k := range data {
			if strings.HasSuffix(k, "_ms") {
				data[k] = 0
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestEventSequencePrivacy (SPEC-07 AC-5 companion, stream level): the
// golden sequence itself carries no absolute paths and no document bodies —
// the privacy defaults hold end-to-end, not just per payload type.
func TestEventSequencePrivacy(t *testing.T) {
	if os.Getenv("SAGE_PARITY_FORCE") == "1" {
		t.Skip("SAGE_PARITY_FORCE=1: regen tests own the build")
	}
	for _, line := range buildEventSequence(t) {
		if strings.Contains(line, "/abs/") || strings.Contains(line, string(filepath.Separator)+"home"+string(filepath.Separator)) {
			t.Errorf("absolute path leaked into event stream: %s", line)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		ws, _ := m["workspace"].(string)
		if strings.ContainsAny(ws, "/\\") {
			t.Errorf("workspace %q is a path, want a name", ws)
		}
	}
}
