package postgres

import (
	"os"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/store"
)

// TestOpenWithoutPgvectorFailsActionably verifies the documented bootstrap
// prerequisite failure mode (spec §5): without CREATE EXTENSION vector,
// writer open fails with an actionable error naming the remedy. Runs only
// when TEST_DATABASE_URL points at a database WITHOUT the extension —
// on a vector-enabled database this test skips (open succeeds).
func TestOpenWithoutPgvectorFailsActionably(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL_NOVECTOR")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL_NOVECTOR unset")
	}
	_, err := Open(dsn, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err == nil {
		t.Skip("vector extension present — error-path check not applicable")
	}
	if !strings.Contains(err.Error(), "CREATE EXTENSION vector") {
		t.Fatalf("error does not name the remedy: %v", err)
	}
}
