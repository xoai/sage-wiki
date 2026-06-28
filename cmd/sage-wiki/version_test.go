package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// stampVersion sets the ldflags-injected vars to known values for the duration
// of a test and restores the build defaults afterward.
func stampVersion(t *testing.T, v, c, d string) {
	t.Helper()
	ov, oc, od := version, commit, date
	version, commit, date = v, c, d
	t.Cleanup(func() { version, commit, date = ov, oc, od })
}

func runVersionCapture(t *testing.T, format string) string {
	t.Helper()
	of := outputFormat
	outputFormat = format
	t.Cleanup(func() { outputFormat = of })

	var buf bytes.Buffer
	versionCmd.SetOut(&buf)
	t.Cleanup(func() { versionCmd.SetOut(nil) })
	if err := runVersion(versionCmd, nil); err != nil {
		t.Fatalf("runVersion: %v", err)
	}
	return buf.String()
}

func TestVersionText(t *testing.T) {
	stampVersion(t, "v9.9.9", "abc1234", "2026-06-28T00:00:00Z")
	got := runVersionCapture(t, "text")
	want := "sage-wiki v9.9.9 (commit abc1234, built 2026-06-28T00:00:00Z)\n"
	if got != want {
		t.Errorf("text output = %q, want %q", got, want)
	}
}

func TestVersionJSON(t *testing.T) {
	stampVersion(t, "v9.9.9", "abc1234", "2026-06-28T00:00:00Z")
	got := runVersionCapture(t, "json")

	var env struct {
		OK   bool              `json:"ok"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, got)
	}
	if !env.OK {
		t.Errorf("envelope ok = false, want true")
	}
	for k, want := range map[string]string{"version": "v9.9.9", "commit": "abc1234", "date": "2026-06-28T00:00:00Z"} {
		if env.Data[k] != want {
			t.Errorf("data.%s = %q, want %q", k, env.Data[k], want)
		}
	}
}

// TestVersionDefaults guards that a plain `go build` (no ldflags) renders
// non-blank placeholders, not empty fields.
func TestVersionDefaults(t *testing.T) {
	stampVersion(t, "dev", "none", "unknown")
	got := runVersionCapture(t, "text")
	want := "sage-wiki dev (commit none, built unknown)\n"
	if got != want {
		t.Errorf("default output = %q, want %q", got, want)
	}
	for _, blank := range []string{"  ", "()", "commit ,", "built )"} {
		if strings.Contains(got, blank) {
			t.Errorf("default output has a blank field (%q): %q", blank, got)
		}
	}
}
