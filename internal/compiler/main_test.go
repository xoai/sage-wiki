package compiler

import (
	"os"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/llm"
)

// TestMain installs a near-zero retry backoff for the whole package:
// provider-failure tests otherwise pay real exponential sleeps (4s, 12s,
// 30s+ per retry branch), which was the dominant cost of this suite
// (~3 min locally, >10 min on Windows CI — the 10m test-timeout failure).
func TestMain(m *testing.M) {
	restoreBackoff := llm.SetBackoffDelayForTest(func(int) time.Duration { return time.Millisecond })
	defer restoreBackoff()
	// Config-driven client construction defaults to 60 RPM (1s spacing per
	// call) — with several LLM calls per test that dominates the suite.
	restoreRate := llm.SetRateLimitForTest(-1)
	defer restoreRate()
	os.Exit(m.Run())
}
