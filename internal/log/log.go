package log

import (
	"fmt"
	"log/slog"
	"os"
)

var logger *slog.Logger

func init() {
	// Default: WARN level — silent during normal operation.
	// Use SetVerbosity(1) for INFO, SetVerbosity(2+) for DEBUG.
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
}

// SetLoggerForTest swaps the package logger and returns a restore func
// (tests only — captures Info/Warn output via slog.SetDefault won't work
// because log keeps its own package-level logger).
func SetLoggerForTest(l *slog.Logger) func() {
	orig := logger
	logger = l
	return func() { logger = orig }
}

// SetVerbose enables info-level logging (single -v).
func SetVerbose(verbose bool) {
	if verbose {
		SetVerbosity(1)
	}
}

// SetVerbosity sets log level by verbosity count.
// 0 = WARN (default), 1 = INFO (-v), 2+ = DEBUG (-vv).
func SetVerbosity(level int) {
	var slogLevel slog.Level
	switch {
	case level >= 2:
		slogLevel = slog.LevelDebug
	case level == 1:
		slogLevel = slog.LevelInfo
	default:
		slogLevel = slog.LevelWarn
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel,
	}))
}

func Debug(msg string, args ...any) { logger.Debug(msg, args...) }
func Info(msg string, args ...any)  { logger.Info(msg, args...) }
func Warn(msg string, args ...any)  { logger.Warn(msg, args...) }
func Error(msg string, args ...any) { logger.Error(msg, args...) }

// Op wraps an error with operation context.
type Op string

// SageError is a structured error with operation and path context.
type SageError struct {
	Op   Op
	Path string
	Err  error
}

func (e *SageError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s [%s]: %s", e.Op, e.Path, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Err)
}

func (e *SageError) Unwrap() error { return e.Err }

// E creates a new SageError.
func E(op Op, err error) *SageError {
	return &SageError{Op: op, Err: err}
}

// EP creates a new SageError with a path.
func EP(op Op, path string, err error) *SageError {
	return &SageError{Op: op, Path: path, Err: err}
}
