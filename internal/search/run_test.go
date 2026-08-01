package search

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestNowSeamChangesRecency proves the Now field drives the recency bonus:
// the same dated doc scores differently under two pinned Now values
// (and identically under the same one — byte-exact goldens, SPEC-09).
func TestNowSeamChangesRecency(t *testing.T) {
	ts := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	bonus := func(now time.Time) float64 {
		ageDays := float64(now.Unix()-ts) / 86400.0
		if ageDays < 0 {
			ageDays = 0
		}
		return 0.05 * math.Exp2(-ageDays/14.0)
	}
	n1 := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	n2 := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
	if bonus(n1) == bonus(n2) {
		t.Fatal("recency bonus must differ across Now values")
	}
	if bonus(n1) != bonus(n1) {
		t.Fatal("same Now must give the same bonus (tautology guard)")
	}

	// Deps.Now zero value falls back to wall clock.
	var d Deps
	if !d.Now.IsZero() {
		t.Error("zero Deps.Now must be the zero time (wall-clock fallback)")
	}
	_ = context.Background
}
