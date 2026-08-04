package engine

import (
	"github.com/xoai/sage-wiki/internal/llm"
)

// usageRecorder builds the compile-time recorder for this Workspace: the
// shared file-ledger + event-sink bridge (SPEC-07 — one implementation per
// behavior; the construction lives in internal/llm).
func (w *Workspace) usageRecorder() llm.UsageRecorder {
	return llm.NewBridgedRecorder(w.dir, w.opts.sink)
}
