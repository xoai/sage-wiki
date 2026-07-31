package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// trackerFromTable loads a workspace table through the registry seam
// (hermetic — never touches the real ~/.sage-wiki/prices.json).
func trackerFromTable(t *testing.T, provider string, override float64, tablePath string) *CostTracker {
	t.Helper()
	r, err := loadRegistryFrom(tablePath, filepath.Join(t.TempDir(), "absent-user.json"))
	if err != nil {
		t.Fatalf("loadRegistryFrom: %v", err)
	}
	return newCostTrackerWithRegistry(provider, override, r)
}

func TestLoadPriceTable_Override(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 9.99, "output": 19.99, "cached_input": 0.99, "batch_input": 4.99, "batch_output": 9.99}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	p, ok := ct.priceFor("gpt-4o")
	if !ok {
		t.Fatal("gpt-4o not priced")
	}
	if p.InputPerMTok.String() != "9.99" || p.OutputPerMTok.String() != "19.99" ||
		p.CachedInputPerMTok.String() != "0.99" || p.BatchInputPerMTok.String() != "4.99" ||
		p.BatchOutputPerMTok.String() != "9.99" {
		t.Errorf("table entry not used: %+v", p)
	}
	if p.Source != PriceSourceUser {
		t.Errorf("Source = %q, want user", p.Source)
	}
}

func TestLoadPriceTable_MergeKeepsBuiltins(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 9.99}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	if p, _ := ct.priceFor("gpt-4o"); p.InputPerMTok.String() != "9.99" {
		t.Errorf("override missing: %+v", p)
	}
	// Unlisted model in the same provider falls back to built-ins.
	if p, _ := ct.priceFor("gpt-4o-mini"); p.InputPerMTok.String() != "0.15" {
		t.Errorf("built-in fallback broken: %+v", p)
	}
	// Another provider's tracker is unaffected (built-ins intact).
	ctAnth := trackerFromTable(t, "anthropic", 0, path)
	if p, _ := ctAnth.priceFor("claude-sonnet-4-20250514"); p.InputPerMTok.String() != "3" {
		t.Errorf("provider fallback broken: %+v", p)
	}
}

func TestLoadPriceTable_MissingFile(t *testing.T) {
	ct := trackerFromTable(t, "openai", 0, filepath.Join(t.TempDir(), "nope.json"))
	if p, ok := ct.priceFor("gpt-4o"); !ok || p.InputPerMTok.String() != "2.5" {
		t.Errorf("missing file should keep built-ins: %+v", p)
	}
}

func TestLoadPriceTable_MalformedJSON(t *testing.T) {
	path := writeTable(t, `{not json`)
	// Malformed workspace table is a HARD ERROR (SPEC-05: no silent fallback).
	if _, err := loadRegistryFrom(path, filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("malformed table must error")
	}
}

func TestLoadPriceTable_PrefixMatching(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 7.0}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	if p, _ := ct.priceFor("gpt-4o-2026-01-01"); p.InputPerMTok.String() != "7" {
		t.Errorf("prefix match on table entry: %+v", p)
	}
}

func TestLoadPriceTable_Precedence(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 7.0}}}`)
	// Explicit override beats table.
	ct := trackerFromTable(t, "openai", 3.0, path)
	if p, _ := ct.priceFor("gpt-4o"); p.InputPerMTok.String() != "3" {
		t.Errorf("TokenPriceOverride must beat table: %+v", p)
	}
}

func TestFormatReport_ApproximateLabel(t *testing.T) {
	ct := newCostTrackerWithRegistry("openai", 0, testRegistry(t))
	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 100}, false)
	out := FormatReport(ct.Report())
	if !strings.Contains(out, "Cost report (approximate)") {
		t.Errorf("report missing approximate label:\n%s", out)
	}
}

func TestEstimateFromBytes_UsesTable(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 10.0, "output": 20.0}}}`)
	_, withTable, err := EstimateFromBytes(1000000, "openai", "gpt-4o", 0, path)
	if err != nil {
		t.Fatalf("EstimateFromBytes: %v", err)
	}
	_, builtIn, err := EstimateFromBytes(1000000, "openai", "gpt-4o", 0, "")
	if err != nil {
		t.Fatalf("EstimateFromBytes: %v", err)
	}
	if withTable == nil || builtIn == nil {
		t.Fatal("gpt-4o must be priced in both cases")
	}
	if !withTable.GreaterThan(*builtIn) {
		t.Errorf("table price (10.0/20.0) should cost more than built-in (2.5/10.0): table=%s builtin=%s", withTable, builtIn)
	}
}

func TestLoadPriceTable_PrefixCollisionLongestWins(t *testing.T) {
	// Table entry and built-in both prefix-match; the LONGEST (most
	// specific) name wins deterministically (built-in "gpt-4o" vs table
	// "gpt-4o-2024-x" for model "gpt-4o-2024-x-y").
	path := writeTable(t, `{"openai": {"gpt-4o-2024-x": {"input": 9.99}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	for i := 0; i < 20; i++ { // map order varies; the result must not
		if p, _ := ct.priceFor("gpt-4o-2024-x-y"); p.InputPerMTok.String() != "9.99" {
			t.Fatalf("collision resolved to built-in: %+v", p)
		}
	}
}

func TestLoadPriceTable_OverlayVsOverlayDeterministic(t *testing.T) {
	// Two table entries both prefix-match; the longest (most specific)
	// name must win every time (map order is random).
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 1.0}, "gpt-4o-2024": {"input": 2.0}}}`)
	ct := trackerFromTable(t, "openai", 0, path)
	for i := 0; i < 20; i++ {
		if p, _ := ct.priceFor("gpt-4o-2024-x"); p.InputPerMTok.String() != "2" {
			t.Fatalf("overlay-vs-overlay resolved nondeterministically: %+v", p)
		}
	}
}
