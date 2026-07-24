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

func TestLoadPriceTable_Override(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 9.99, "output": 19.99, "cached_input": 0.99, "batch_input": 4.99, "batch_output": 9.99}}}`)
	ct := NewCostTrackerWithTable("openai", 0, path)
	p := ct.getPrice("gpt-4o")
	if p.Input != 9.99 || p.Output != 19.99 || p.CachedInput != 0.99 || p.BatchInput != 4.99 || p.BatchOutput != 9.99 {
		t.Errorf("table entry not used: %+v", p)
	}
}

func TestLoadPriceTable_MergeKeepsBuiltins(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 9.99}}}`)
	ct := NewCostTrackerWithTable("openai", 0, path)
	// Named entry overridden…
	if p := ct.getPrice("gpt-4o"); p.Input != 9.99 {
		t.Errorf("override missing: %+v", p)
	}
	// …unlisted model in the same provider falls back to built-ins.
	if p := ct.getPrice("gpt-4o-mini"); p.Input != 0.15 {
		t.Errorf("built-in fallback broken: %+v", p)
	}
	// …another provider's tracker is unaffected (built-ins intact).
	ctAnth := NewCostTrackerWithTable("anthropic", 0, path)
	if p := ctAnth.getPrice("claude-sonnet-4-20250514"); p.Input != 3.0 {
		t.Errorf("provider fallback broken: %+v", p)
	}
}

func TestLoadPriceTable_MissingFile(t *testing.T) {
	ct := NewCostTrackerWithTable("openai", 0, filepath.Join(t.TempDir(), "nope.json"))
	if p := ct.getPrice("gpt-4o"); p.Input != 2.5 {
		t.Errorf("missing file should keep built-ins: %+v", p)
	}
}

func TestLoadPriceTable_MalformedJSON(t *testing.T) {
	path := writeTable(t, `{not json`)
	ct := NewCostTrackerWithTable("openai", 0, path)
	if p := ct.getPrice("gpt-4o"); p.Input != 2.5 {
		t.Errorf("malformed file should keep built-ins: %+v", p)
	}
}

func TestLoadPriceTable_PrefixMatching(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 7.0}}}`)
	ct := NewCostTrackerWithTable("openai", 0, path)
	// Versioned variant matches by prefix, same as built-ins.
	if p := ct.getPrice("gpt-4o-2026-01-01"); p.Input != 7.0 {
		t.Errorf("prefix match on table entry: %+v", p)
	}
}

func TestLoadPriceTable_Precedence(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 7.0}}}`)
	// Explicit override beats table.
	ct := NewCostTrackerWithTable("openai", 3.0, path)
	if p := ct.getPrice("gpt-4o"); p.Input != 3.0 {
		t.Errorf("TokenPriceOverride must beat table: %+v", p)
	}
}

func TestLoadPriceTable_OldSignatureUnchanged(t *testing.T) {
	ct := NewCostTracker("openai", 0)
	if p := ct.getPrice("gpt-4o"); p.Input != 2.5 {
		t.Errorf("old constructor must use built-ins: %+v", p)
	}
}

func TestFormatReport_ApproximateLabel(t *testing.T) {
	ct := NewCostTracker("openai", 0)
	ct.Track("summarize", "gpt-4o", Usage{InputTokens: 1000, OutputTokens: 100}, false)
	out := FormatReport(ct.Report())
	if !strings.Contains(out, "Cost report (approximate)") {
		t.Errorf("report missing approximate label:\n%s", out)
	}
}

func TestEstimateFromBytes_UsesTable(t *testing.T) {
	path := writeTable(t, `{"openai": {"gpt-4o": {"input": 10.0, "output": 20.0}}}`)
	_, withTable := EstimateFromBytes(1000000, "openai", "gpt-4o", 0, path)
	_, builtIn := EstimateFromBytes(1000000, "openai", "gpt-4o", 0, "")
	if withTable <= builtIn {
		t.Errorf("table price (10.0/20.0) should cost more than built-in (2.5/10.0): table=%f builtin=%f", withTable, builtIn)
	}
}

func TestLoadPriceTable_PrefixCollisionOverlayWins(t *testing.T) {
	// Table entry and built-in both prefix-match; the table must win
	// deterministically (built-in "gpt-4o" vs table "gpt-4o-2024-x" for
	// model "gpt-4o-2024-x-y").
	path := writeTable(t, `{"openai": {"gpt-4o-2024-x": {"input": 9.99}}}`)
	ct := NewCostTrackerWithTable("openai", 0, path)
	for i := 0; i < 20; i++ { // map order varies; the result must not
		if p := ct.getPrice("gpt-4o-2024-x-y"); p.Input != 9.99 {
			t.Fatalf("collision resolved to built-in: %+v", p)
		}
	}
}
