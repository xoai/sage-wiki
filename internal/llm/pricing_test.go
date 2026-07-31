package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryLookup_Exact(t *testing.T) {
	r := mustLoadBuiltin(t)
	p, ok := r.Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("openai:gpt-4o not found in builtin registry")
	}
	if p.InputPerMTok == nil || p.InputPerMTok.String() != "2.5" {
		t.Errorf("InputPerMTok = %v, want 2.5", p.InputPerMTok)
	}
	if p.Source != "builtin" {
		t.Errorf("Source = %q, want builtin", p.Source)
	}
	if p.AsOf.IsZero() {
		t.Error("AsOf must be set on builtin entries")
	}
}

func TestRegistryLookup_PrefixWithinProvider(t *testing.T) {
	r := mustLoadBuiltin(t)
	p, ok := r.Lookup("openai", "gpt-4o-2024-08-06")
	if !ok {
		t.Fatal("openai:gpt-4o-2024-08-06 should prefix-match gpt-4o")
	}
	if p.InputPerMTok.String() != "2.5" {
		t.Errorf("InputPerMTok = %v, want 2.5 (gpt-4o)", p.InputPerMTok)
	}
}

// TestRegistryLookup_NoCrossProvider is the SPEC-05 defect guard: a model
// name that exists under one provider must never resolve under another.
func TestRegistryLookup_NoCrossProvider(t *testing.T) {
	r := mustLoadBuiltin(t)
	if _, ok := r.Lookup("openai-compatible", "gpt-4o"); ok {
		t.Error("openai-compatible:gpt-4o must NOT resolve to OpenAI's price")
	}
	if _, ok := r.Lookup("ollama", "gpt-4o-mini"); ok {
		t.Error("ollama:gpt-4o-mini must NOT resolve to OpenAI's price")
	}
}

func TestRegistryLookup_Unknown(t *testing.T) {
	r := mustLoadBuiltin(t)
	if _, ok := r.Lookup("openai-compatible", "deepseek-v4-flash"); ok {
		t.Error("unknown model must not be found (nil cost, never a guess)")
	}
}

func TestRegistryLookup_DeepSeekBuiltin(t *testing.T) {
	r := mustLoadBuiltin(t)
	p, ok := r.Lookup("openai-compatible", "deepseek-chat")
	if !ok {
		t.Fatal("openai-compatible:deepseek-chat should be in builtin registry")
	}
	if p.CachedInputPerMTok == nil {
		t.Error("deepseek-chat must carry a cached-input price (prefix-cache economics)")
	}
}

func TestLoadRegistry_Precedence(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	wsPath := filepath.Join(dir, "ws.json")
	writeFile(t, userPath, `{"prices": {"openai:gpt-4o": {"input": "9.9", "output": "9.9", "as_of": "2026-01-01"}}}`)
	writeFile(t, wsPath, `{"prices": {"openai:gpt-4o": {"input": "7.7", "output": "7.7", "as_of": "2026-02-01"}}}`)

	r, err := loadRegistryFrom(wsPath, userPath)
	if err != nil {
		t.Fatalf("loadRegistryFrom: %v", err)
	}
	p, _ := r.Lookup("openai", "gpt-4o")
	if p.InputPerMTok.String() != "7.7" {
		t.Errorf("workspace file should win: InputPerMTok = %v, want 7.7", p.InputPerMTok)
	}
	if p.Source != "user" {
		t.Errorf("Source = %q, want user", p.Source)
	}

	// User file wins over builtin when workspace doesn't name the model.
	r2, err := loadRegistryFrom("", userPath)
	if err != nil {
		t.Fatalf("loadRegistryFrom: %v", err)
	}
	p2, _ := r2.Lookup("openai", "gpt-4o")
	if p2.InputPerMTok.String() != "9.9" {
		t.Errorf("user file should beat builtin: InputPerMTok = %v, want 9.9", p2.InputPerMTok)
	}
}

// TestLoadRegistry_LegacyShape verifies the PERF-04 workspace table shape
// (provider → model → float fields) still loads, as Source "user".
func TestLoadRegistry_LegacyShape(t *testing.T) {
	dir := t.TempDir()
	wsPath := filepath.Join(dir, "legacy.json")
	writeFile(t, wsPath, `{"openai": {"gpt-4o": {"input": 5.0, "output": 12.0, "cached_input": 2.5}}}`)

	r, err := loadRegistryFrom(wsPath, filepath.Join(dir, "absent.json"))
	if err != nil {
		t.Fatalf("loadRegistryFrom legacy: %v", err)
	}
	p, ok := r.Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("legacy entry not loaded")
	}
	if p.InputPerMTok.String() != "5" {
		t.Errorf("InputPerMTok = %v, want 5", p.InputPerMTok)
	}
	if p.Source != "user" {
		t.Errorf("Source = %q, want user", p.Source)
	}
}

func TestLoadRegistry_MalformedError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, `{not json`)
	if _, err := loadRegistryFrom(bad, filepath.Join(dir, "absent.json")); err == nil {
		t.Fatal("malformed workspace price file must be a hard error, not silent fallback")
	}
}

func TestLoadRegistry_MissingFilesOK(t *testing.T) {
	dir := t.TempDir()
	r, err := loadRegistryFrom("", filepath.Join(dir, "absent.json"))
	if err != nil {
		t.Fatalf("missing files must not error: %v", err)
	}
	if _, ok := r.Lookup("openai", "gpt-4o"); !ok {
		t.Error("builtins must still be present")
	}
}

// TestDefaultPrices_HasAsOfAndComment is AC-A5: embedded defaults carry
// as_of dates and a header comment stating they are estimates requiring
// user verification.
func TestDefaultPrices_HasAsOfAndComment(t *testing.T) {
	raw, err := defaultPricesJSON()
	if err != nil {
		t.Fatalf("defaultPricesJSON: %v", err)
	}
	var doc struct {
		Comment string                    `json:"_comment"`
		Prices  map[string]jsonPriceEntry `json:"prices"`
	}
	if err := unmarshalJSON(raw, &doc); err != nil {
		t.Fatalf("default.json malformed: %v", err)
	}
	if doc.Comment == "" {
		t.Error("default.json must carry a _comment header stating prices are estimates requiring verification")
	}
	if len(doc.Prices) == 0 {
		t.Fatal("default.json has no entries")
	}
	for key, e := range doc.Prices {
		if e.AsOf == "" {
			t.Errorf("entry %s missing as_of", key)
		}
	}
}

func TestRegistryEntries_Sorted(t *testing.T) {
	r := mustLoadBuiltin(t)
	entries := r.Entries()
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Key >= entries[i].Key {
			t.Fatalf("Entries not sorted: %q >= %q", entries[i-1].Key, entries[i].Key)
		}
	}
}

func mustLoadBuiltin(t *testing.T) *Registry {
	t.Helper()
	r, err := loadRegistryFrom("", filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("loadRegistryFrom: %v", err)
	}
	return r
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRegistryLookup_ModelPrefixOfKeyNeverMatches pins F-034: a model ID
// that is merely a prefix of a registry key must resolve unknown, not
// inherit that key's price.
func TestRegistryLookup_ModelPrefixOfKeyNeverMatches(t *testing.T) {
	r := mustLoadBuiltin(t)
	if _, ok := r.Lookup("openai", "gpt"); ok {
		t.Error("'gpt' must not inherit gpt-4o-mini's price")
	}
	if _, ok := r.Lookup("gemini", "gemini-2.5"); ok {
		t.Error("'gemini-2.5' must not inherit gemini-2.5-flash's price")
	}
}
