package events

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxBytes  int64 = 10 << 20 // 10 MiB per generation
	defaultKeepFiles       = 5
	filePrefix             = "events-"
	fileSuffix             = ".jsonl"
)

// JSONLFileSink is the durable audit-trail sink: one JSON object per line,
// rotating at a size threshold, keeping the newest N generations. Failure
// policy (stated): a write failure is logged and the event dropped — the
// audit trail is best-effort telemetry and must never block or panic the
// engine.
type JSONLFileSink struct {
	dir       string
	maxBytes  int64
	keepFiles int
	logger    *slog.Logger

	mu      sync.Mutex
	file    *os.File
	written int64
	closed  bool
}

// FileSinkOption configures a JSONLFileSink.
type FileSinkOption func(*JSONLFileSink)

// WithMaxBytes sets the rotation threshold (bytes per generation file).
func WithMaxBytes(n int64) FileSinkOption {
	return func(s *JSONLFileSink) {
		if n >= 1 {
			s.maxBytes = n
		}
	}
}

// WithKeepFiles sets how many generations are retained past rotation.
func WithKeepFiles(n int) FileSinkOption {
	return func(s *JSONLFileSink) {
		if n >= 1 {
			s.keepFiles = n
		}
	}
}

// WithFileLogger sets the logger for write-failure diagnostics.
func WithFileLogger(l *slog.Logger) FileSinkOption {
	return func(s *JSONLFileSink) { s.logger = l }
}

// NewJSONLFileSink builds a sink writing events-<ts>.jsonl generations
// under dir. The directory is created lazily on first write.
func NewJSONLFileSink(dir string, opts ...FileSinkOption) *JSONLFileSink {
	s := &JSONLFileSink{
		dir:       dir,
		maxBytes:  defaultMaxBytes,
		keepFiles: defaultKeepFiles,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Emit writes one JSON line. Rotation happens before the write that would
// cross the threshold. Every failure degrades to log + drop.
func (s *JSONLFileSink) Emit(ev Event) {
	raw, err := json.Marshal(ev)
	if err != nil {
		s.warn("marshal event failed — dropping", err)
		return
	}
	raw = append(raw, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if err := s.ensureOpen(len(raw)); err != nil {
		s.warn("open events file failed — dropping event", err)
		return
	}
	if _, err := s.file.Write(raw); err != nil {
		s.warn("write events file failed — dropping event", err)
		return
	}
	s.written += int64(len(raw))
	// Best-effort fsync per write batch (SPEC-07 §3): durability without
	// failing the pipeline when the filesystem can't promise it.
	_ = s.file.Sync()
}

// Close flushes and closes the current generation. Later Emits drop.
func (s *JSONLFileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// ensureOpen opens the first generation or rotates when the next write
// would cross the threshold. Caller holds mu.
func (s *JSONLFileSink) ensureOpen(nextLen int) error {
	if s.file != nil {
		if s.written > 0 && s.written+int64(nextLen) > s.maxBytes {
			if err := s.file.Close(); err != nil {
				s.warn("close generation failed", err)
			}
			s.file = nil
			s.written = 0
			s.prune()
		} else {
			return nil
		}
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	f, err := openGeneration(s.dir)
	if err != nil {
		return err
	}
	s.file = f
	return nil
}

// openGeneration creates a fresh uniquely-named generation file. The name
// is a zero-padded unix-nano timestamp so lexicographic order == creation
// order (pruning sorts by name).
func openGeneration(dir string) (*os.File, error) {
	name := fmt.Sprintf("%s%020d%s", filePrefix, time.Now().UnixNano(), fileSuffix)
	for i := 0; i < 1000; i++ {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("open generation %s: %w", name, err)
		}
		name = fmt.Sprintf("%s%020d%s", filePrefix, time.Now().UnixNano()+int64(i)+1, fileSuffix)
	}
	return nil, fmt.Errorf("open generation: name exhausted in %s", dir)
}

// prune removes generations beyond keepFiles, newest kept. Caller holds mu.
func (s *JSONLFileSink) prune() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.warn("prune: read dir failed", err)
		return
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, filePrefix) && strings.HasSuffix(n, fileSuffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names) // zero-padded timestamp names: lex order == age order
	for len(names) >= s.keepFiles {
		oldest := names[0]
		names = names[1:]
		if err := os.Remove(filepath.Join(s.dir, oldest)); err != nil {
			s.warn("prune: remove old generation failed", err)
		}
	}
}

func (s *JSONLFileSink) warn(msg string, err error) {
	if s.logger != nil {
		s.logger.Warn("events: "+msg, "dir", s.dir, "error", err)
		return
	}
	slog.Warn("events: "+msg, "dir", s.dir, "error", err)
}
