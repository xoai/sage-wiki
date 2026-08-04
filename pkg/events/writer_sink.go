package events

import (
	"encoding/json"
	"io"
	"sync"
)

// WriterSink emits one JSON object per line to any io.Writer — the stdout
// sink for piping (`events.stdout`). Write failures drop the event: a dead
// pipe must never stall the engine. The caller owns the writer; Close does
// not close it (os.Stdout must survive the sink).
type WriterSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriterSink builds a sink over w.
func NewWriterSink(w io.Writer) *WriterSink {
	return &WriterSink{w: w}
}

// Emit writes one JSON line.
func (s *WriterSink) Emit(ev Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(raw)
}

// Close is a no-op: the caller owns the underlying writer.
func (s *WriterSink) Close() error {
	return nil
}
