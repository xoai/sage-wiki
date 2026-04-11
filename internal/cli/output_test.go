package cli

import (
	"encoding/json"
	"testing"
)

func TestJSONSuccess(t *testing.T) {
	data := map[string]int{"count": 5}
	out := FormatJSON(true, data, "")
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !resp.OK {
		t.Error("expected ok=true")
	}
}

func TestJSONError(t *testing.T) {
	out := FormatJSON(false, nil, "something failed")
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
	if resp.Error != "something failed" {
		t.Errorf("expected error message, got %q", resp.Error)
	}
}

func TestOutputDispatch(t *testing.T) {
	// format=json -> JSON output
	got := Output("json", "text fallback", true, map[string]int{"n": 1}, "")
	var resp Response
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("json dispatch failed: %v", err)
	}

	// format=text -> text output
	got = Output("text", "text fallback", true, nil, "")
	if got != "text fallback" {
		t.Errorf("text dispatch: expected fallback, got %q", got)
	}
}
