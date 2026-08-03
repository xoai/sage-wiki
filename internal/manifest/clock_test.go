package manifest

import (
	"testing"
	"time"
)

var manifestFixedClock = func() time.Time {
	return time.Unix(1700000000, 0).UTC()
}

func TestManifestSetNow_AddSource(t *testing.T) {
	m := New()
	m.SetNow(manifestFixedClock)
	m.AddSource("raw/a.md", "sha256:abc", "note", 12)
	got := m.Sources["raw/a.md"].AddedAt
	want := "2023-11-14T22:13:20Z"
	if got != want {
		t.Errorf("AddedAt = %q, want %q (injected clock)", got, want)
	}
}

func TestManifestSetNow_MarkCompiled(t *testing.T) {
	m := New()
	m.AddSource("raw/a.md", "sha256:abc", "note", 12)
	m.SetNow(manifestFixedClock)
	m.MarkCompiled("raw/a.md", "wiki/summaries/raw-a.md", []string{"alpha"})
	got := m.Sources["raw/a.md"].CompiledAt
	want := "2023-11-14T22:13:20Z"
	if got != want {
		t.Errorf("CompiledAt = %q, want %q (injected clock)", got, want)
	}
}

func TestManifestSetNow_AddConcept(t *testing.T) {
	m := New()
	m.SetNow(manifestFixedClock)
	m.AddConcept("alpha", "wiki/concepts/alpha.md", []string{"raw/a.md"})
	got := m.Concepts["alpha"].LastCompiled
	want := "2023-11-14T22:13:20Z"
	if got != want {
		t.Errorf("LastCompiled = %q, want %q (injected clock)", got, want)
	}
}

func TestManifestNewWithClock(t *testing.T) {
	m := NewWithClock(manifestFixedClock)
	want := "2023-11-14T22:13:20Z"
	if m.CreatedAt != want {
		t.Errorf("CreatedAt = %q, want %q (injected clock)", m.CreatedAt, want)
	}
}
