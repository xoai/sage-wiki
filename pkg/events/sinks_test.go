package events

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestJSONLFileSinkWritesValidJSONL: one JSON object per line, envelope
// intact.
func TestJSONLFileSinkWritesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	sink := NewJSONLFileSink(dir)
	for i := 0; i < 3; i++ {
		sink.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: int64(i)}))
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	files := jsonlFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("files = %v, want exactly 1", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(lines))
	}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		if ev.Type != TypeEventsDropped || ev.Workspace != "ws" {
			t.Errorf("line %d envelope wrong: %+v", i, ev)
		}
	}
}

// TestJSONLFileSinkRotatesAtThreshold: crossing the size threshold starts a
// new generation file.
func TestJSONLFileSinkRotatesAtThreshold(t *testing.T) {
	dir := t.TempDir()
	sink := NewJSONLFileSink(dir, WithMaxBytes(300), WithKeepFiles(50))
	for i := 0; i < 20; i++ {
		sink.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i), Bytes: 1}))
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	files := jsonlFiles(t, dir)
	if len(files) < 2 {
		t.Fatalf("files = %v, want >= 2 generations after rotation at 300 bytes", files)
	}
	total := 0
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 300+200 { // threshold + one line overshoot tolerance
			t.Errorf("%s is %d bytes — rotation did not bound size", f, info.Size())
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		total += len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
	}
	if total != 20 {
		t.Fatalf("total lines across generations = %d, want 20 (no event lost)", total)
	}
}

// TestJSONLFileSinkPrunesPastKeepFiles: generations beyond keep-N are
// pruned, newest kept.
func TestJSONLFileSinkPrunesPastKeepFiles(t *testing.T) {
	dir := t.TempDir()
	sink := NewJSONLFileSink(dir, WithMaxBytes(200), WithKeepFiles(2))
	for i := 0; i < 40; i++ {
		sink.Emit(NewEvent("ws", TypeMirrorShipped, MirrorShipped{Generation: int64(i)}))
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	files := jsonlFiles(t, dir)
	if len(files) > 2 {
		t.Fatalf("files = %v, want at most 2 generations retained", files)
	}
	// The retained generations are the NEWEST: the last emitted event
	// must be present.
	var foundLast bool
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"generation":39`) {
			foundLast = true
		}
	}
	if !foundLast {
		t.Fatal("pruning removed the newest generation")
	}
}

// TestJSONLFileSinkUnwritableDirDrops: a sink whose directory cannot be
// created logs and drops — never blocks, never panics.
func TestJSONLFileSinkUnwritableDirDrops(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory path UNDER a regular file can never be created.
	sink := NewJSONLFileSink(filepath.Join(blocker, "events"))
	for i := 0; i < 5; i++ {
		sink.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 1})) // must not panic
	}
	_ = sink.Close()
}

// TestJSONLFileSinkConcurrentEmit: concurrent emitters are safe (-race).
func TestJSONLFileSinkConcurrentEmit(t *testing.T) {
	dir := t.TempDir()
	sink := NewJSONLFileSink(dir, WithMaxBytes(4096), WithKeepFiles(20))
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				sink.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: int64(g*100 + i)}))
			}
		}(g)
	}
	wg.Wait()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range jsonlFiles(t, dir) {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				t.Fatalf("corrupt line under concurrency: %v", err)
			}
			total++
		}
	}
	if total != 150 {
		t.Fatalf("lines = %d, want 150", total)
	}
}

// TestWriterSink: JSONL to any io.Writer (the stdout sink).
func TestWriterSink(t *testing.T) {
	var buf bytes.Buffer
	sink := NewWriterSink(&buf)
	sink.Emit(NewEvent("ws", TypeEventsDropped, EventsDropped{Dropped: 4}))
	var ev Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("writer output not one JSON object: %v", err)
	}
	if ev.Type != TypeEventsDropped {
		t.Errorf("type = %s", ev.Type)
	}
}

func jsonlFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
