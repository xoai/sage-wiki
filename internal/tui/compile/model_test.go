package compile

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/compiler"
)

func TestTUICompileEvents(t *testing.T) {
	m := New(t.TempDir(), "wiki", []string{"raw"}, 2)
	m.compiling = true

	// Phase event resets counters.
	updated, cmd := m.Update(compileEventMsg{ev: compiler.ProgressEvent{Type: "phase", Phase: "Tier 1", Total: 3}})
	m = updated.(Model)
	if m.itemsTotal != 3 || m.itemsDone != 0 {
		t.Errorf("phase event: total=%d done=%d, want 3/0", m.itemsTotal, m.itemsDone)
	}
	if cmd == nil {
		t.Error("event pump not re-armed while compiling")
	}

	// Item event updates the live line.
	updated, _ = m.Update(compileEventMsg{ev: compiler.ProgressEvent{Type: "item", Item: "raw/a.md", Status: "done", Done: 1, Total: 3}})
	m = updated.(Model)
	if m.currentItem != "raw/a.md" || m.itemsDone != 1 {
		t.Errorf("item event: item=%q done=%d", m.currentItem, m.itemsDone)
	}
	if got := m.statusInfo(); got != "Compiling [1/3] raw/a.md" {
		t.Errorf("statusInfo = %q", got)
	}

	// Completion clears the live state.
	updated, _ = m.Update(CompileCompleteMsg{})
	m = updated.(Model)
	if m.compiling || m.currentItem != "" {
		t.Errorf("after complete: compiling=%v item=%q", m.compiling, m.currentItem)
	}
}
