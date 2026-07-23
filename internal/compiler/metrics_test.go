package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

func TestBackpressureGauges(t *testing.T) {
	metrics.ResetForTest()
	bc := NewBackpressureController(4)
	release := bc.Acquire()
	snap := metrics.Snapshot()
	var limit, flight int64
	for i := 0; i+1 < len(snap); i += 2 {
		switch snap[i] {
		case "compile_backpressure_limit":
			limit = snap[i+1].(int64)
		case "compile_backpressure_in_flight":
			flight = snap[i+1].(int64)
		}
	}
	if limit != 4 {
		t.Errorf("limit = %d, want 4", limit)
	}
	if flight != 1 {
		t.Errorf("in_flight = %d, want 1", flight)
	}
	release()
	snap = metrics.Snapshot()
	for i := 0; i+1 < len(snap); i += 2 {
		if snap[i] == "compile_backpressure_in_flight" {
			if snap[i+1].(int64) != 0 {
				t.Errorf("in_flight after release = %v, want 0", snap[i+1])
			}
		}
	}
}
