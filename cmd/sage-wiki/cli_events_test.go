package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// TestCLISearchEmitsSearchPerformed: the CLI search path (cliEventPlane +
// read-only engine open, exactly runSearch's wiring) writes
// search_performed into the audit trail — searches are observable too, not
// just mutations (SPEC-07 wiring clause).
func TestCLISearchEmitsSearchPerformed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ws")
	if err := wiki.InitGreenfield(dir, "clisearch", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	evOpts, evClose := cliEventPlane(ctx, dir)
	w, err := engine.Open(ctx, dir, append([]engine.Option{engine.WithReadOnly()}, evOpts...)...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Search(ctx, engine.SearchRequest{Query: "anything"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	evClose() // flush the bus before reading the audit trail

	entries, err := os.ReadDir(filepath.Join(dir, "events"))
	if err != nil {
		t.Fatalf("events dir missing — audit trail not wired: %v", err)
	}
	var found bool
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, "events", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"type":"search_performed"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("no search_performed event in the CLI audit trail")
	}
}
