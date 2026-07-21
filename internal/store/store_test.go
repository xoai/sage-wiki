package store

import (
	"errors"
	"regexp"
	"testing"
	"time"
)

func TestLearningIDDeterministic(t *testing.T) {
	id1 := LearningID("redis eviction is LRU here")
	id2 := LearningID("redis eviction is LRU here")
	if id1 != id2 {
		t.Errorf("LearningID not deterministic: %q vs %q", id1, id2)
	}
	if id1 == LearningID("different content") {
		t.Error("LearningID collision on different content")
	}
	if !regexp.MustCompile(`^learn-[0-9a-f]{16}$`).MatchString(id1) {
		t.Errorf("LearningID format = %q, want learn-<16 lowercase hex>", id1)
	}
}

func TestSentinelsDistinct(t *testing.T) {
	for i, a := range []error{ErrReadOnly, ErrWriterActive, ErrSchemaVersion, ErrDimensionMismatch, ErrNotFound} {
		for j, b := range []error{ErrReadOnly, ErrWriterActive, ErrSchemaVersion, ErrDimensionMismatch, ErrNotFound} {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinels %d and %d alias", i, j)
			}
		}
	}
}

func TestOpenOptionsDefaults(t *testing.T) {
	opts := OpenOptions{}
	if opts.Mode != ModeWriter {
		t.Errorf("zero Mode = %v, want ModeWriter (default)", opts.Mode)
	}
	opts = OpenOptions{LockTimeout: 5 * time.Second, Pool: PoolConfig{MaxOpen: 10, MaxIdle: 2}}
	if opts.Pool.MaxOpen != 10 || opts.Pool.MaxIdle != 2 {
		t.Errorf("pool = %+v", opts.Pool)
	}
}
