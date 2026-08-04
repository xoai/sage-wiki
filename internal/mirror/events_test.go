package mirror

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/pkg/events"
)

type mirrorCaptureSink struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *mirrorCaptureSink) Emit(ev events.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

// TestEmitShipped: a successful ship pass fans mirror_shipped with the
// generation and the sealed bytes; nil sink is a no-op.
func TestEmitShipped(t *testing.T) {
	dir := t.TempDir()
	sink := &mirrorCaptureSink{}
	m := &Mirror{dir: dir, sink: sink}
	m.local = &LocalState{Generation: 7}

	m.emitShipped(shipResult{BytesShipped: 4096})

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Type != events.TypeMirrorShipped {
		t.Fatalf("type = %s", ev.Type)
	}
	if ev.Workspace != filepath.Base(dir) {
		t.Errorf("Workspace = %q, want %q", ev.Workspace, filepath.Base(dir))
	}
	d := ev.Data.(events.MirrorShipped)
	if d.Generation != 7 || d.Bytes != 4096 {
		t.Errorf("payload = %+v", d)
	}

	// Nil sink: no panic.
	m2 := &Mirror{dir: dir}
	m2.local = &LocalState{Generation: 1}
	m2.emitShipped(shipResult{})
}

// TestEmitSnapshot: a rotation fans mirror_snapshot with the new
// generation and the byte size rotate() recorded.
func TestEmitSnapshot(t *testing.T) {
	sink := &mirrorCaptureSink{}
	m := &Mirror{dir: t.TempDir(), sink: sink}
	m.lastSnapshotBytes.Store(512)

	m.emitSnapshot(9)

	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	d := sink.events[0].Data.(events.MirrorSnapshot)
	if d.Generation != 9 {
		t.Errorf("payload = %+v", d)
	}
	if d.Bytes != 512 {
		t.Errorf("Bytes = %d, want 512 (rotate's recorded snapshot size)", d.Bytes)
	}
}
