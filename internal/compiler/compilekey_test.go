package compiler

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
)

func keyTestConfig() *config.Config {
	base := config.Defaults()
	cfg := &base
	cfg.Project = "keytest"
	cfg.Output = "wiki"
	cfg.Sources = []config.Source{{Path: "raw"}}
	cfg.API.Provider = "openai"
	cfg.Models.Summarize = "gpt-4o-mini"
	return cfg
}

// TestCompileKey_Golden pins the exact key + parts for a fixture config
// (spec test 5: compile-key composition golden, tier≥3 and tier<3 shapes).
func TestCompileKey_Golden(t *testing.T) {
	cfg := keyTestConfig()
	parts, err := ComputeCompileKeyParts("sha256:abc123", 3, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	if parts.Source != "sha256:abc123" {
		t.Errorf("Source = %q", parts.Source)
	}
	if parts.Pipeline != "1" {
		t.Errorf("Pipeline = %q, want 1", parts.Pipeline)
	}
	if parts.Templates == "" || parts.Models == "" {
		t.Error("tier≥3 parts must carry templates and models components")
	}
	if !strings.Contains(parts.Templates, "write_article@1.0.0:") {
		t.Errorf("templates component missing versioned write_article: %q", parts.Templates)
	}
	if !strings.Contains(parts.Models, "summarize=gpt-4o-mini") {
		t.Errorf("models component missing resolved summarize model: %q", parts.Models)
	}
	if !strings.HasPrefix(parts.Embed, "openai:") {
		t.Errorf("embed identity = %q, want openai:...", parts.Embed)
	}
	key := parts.Key(3)
	if len(key) != 64 {
		t.Errorf("key length = %d, want 64 hex chars", len(key))
	}

	// tier<3: no templates/models; config = chunk subset hash; key differs.
	partsLow, err := ComputeCompileKeyParts("sha256:abc123", 1, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if partsLow.Templates != "" || partsLow.Models != "" {
		t.Errorf("tier<3 parts must have empty templates/models, got %q / %q", partsLow.Templates, partsLow.Models)
	}
	if partsLow.Key(1) == key {
		t.Error("tier<3 key equals tier≥3 key — shapes must differ")
	}
}

// TestDriftClass_FirstDifferingComponent pins the attribution order.
func TestDriftClass_FirstDifferingComponent(t *testing.T) {
	base, err := ComputeCompileKeyParts("sha256:a", 3, keyTestConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}

	other := base
	other.Source = "sha256:b"
	if got := DriftClass(base, other); got != "content" {
		t.Errorf("source drift: got %q, want content", got)
	}
	other = base
	other.Pipeline = "2"
	if got := DriftClass(base, other); got != "pipeline" {
		t.Errorf("pipeline drift: got %q, want pipeline", got)
	}
	other = base
	other.Templates += "x"
	if got := DriftClass(base, other); got != "templates" {
		t.Errorf("templates drift: got %q, want templates", got)
	}
	other = base
	other.Models += "x"
	if got := DriftClass(base, other); got != "models" {
		t.Errorf("models drift: got %q, want models", got)
	}
	other = base
	other.Config += "x"
	if got := DriftClass(base, other); got != "config" {
		t.Errorf("config drift: got %q, want config", got)
	}
	other = base
	other.Embed += "x"
	if got := DriftClass(base, other); got != "embed" {
		t.Errorf("embed drift: got %q, want embed", got)
	}
	if got := DriftClass(base, base); got != "" {
		t.Errorf("identical parts: got %q, want empty", got)
	}
}

// TestModelKey_ResolutionChains pins each pass's fallback chain against the
// same config mutations the passes see.
func TestModelKey_ResolutionChains(t *testing.T) {
	cfg := keyTestConfig()
	cfg.Models.Summarize = ""
	cfg.Models.Extract = "ext-model"
	parts, err := ComputeCompileKeyParts("sha256:x", 3, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// summarize falls to the hardcoded default; extract explicit; write/triples/resolve/communities follow extract→summarize chain.
	if !strings.Contains(parts.Models, "summarize=gpt-4o-mini") {
		t.Errorf("summarize fallback: %q", parts.Models)
	}
	if !strings.Contains(parts.Models, "extract=ext-model") {
		t.Errorf("extract explicit: %q", parts.Models)
	}
	if !strings.Contains(parts.Models, "write=gpt-4o-mini") {
		t.Errorf("write follows summarize-resolved (which fell to default): %q", parts.Models)
	}
	if !strings.Contains(parts.Models, "triples=ext-model") {
		t.Errorf("triples follows extract: %q", parts.Models)
	}
}

// TestCompileKey_ConfigDriftSensitivity: a subset field change rekeys; an
// ignored field change must NOT.
func TestCompileKey_ConfigDriftSensitivity(t *testing.T) {
	cfg1 := keyTestConfig()
	p1, err := ComputeCompileKeyParts("sha256:a", 3, cfg1, nil)
	if err != nil {
		t.Fatal(err)
	}
	k1 := p1.Key(3)

	cfg2 := keyTestConfig()
	cfg2.Compiler.DedupThreshold = 0.9
	p2, err := ComputeCompileKeyParts("sha256:a", 3, cfg2, nil)
	if err != nil {
		t.Fatal(err)
	}
	k2 := p2.Key(3)
	if k1 == k2 {
		t.Error("dedup_threshold change did not rekey (subset field)")
	}

	cfg3 := keyTestConfig()
	cfg3.Serve.Token = "supersecret"
	p3, err := ComputeCompileKeyParts("sha256:a", 3, cfg3, nil)
	if err != nil {
		t.Fatal(err)
	}
	k3 := p3.Key(3)
	if k1 != k3 {
		t.Error("serve.token change rekeyed — ignored fields must not affect the key")
	}
}

// TestConfigSubset_ReflectionGuard: every config leaf has a policy
// disposition, every policy entry is a real leaf, and every "include" leaf
// appears in the subset JSON. THIS is what keeps the key complete forever.
func TestConfigSubset_ReflectionGuard(t *testing.T) {
	leaves := configLeafPaths()
	policy := policyDispositionsForTest()

	for _, leaf := range leaves {
		if _, ok := policy[leaf]; !ok {
			t.Errorf("config leaf %q has NO policy disposition — add it to subsetPolicy with include or a justification", leaf)
		}
	}
	for key := range policy {
		found := false
		for _, leaf := range leaves {
			if leaf == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("policy entry %q is not a config leaf (stale or typo)", key)
		}
	}

	subset := compileConfigSubset(keyTestConfig())
	subsetJSON, err := canonicalSubsetJSON(subset)
	if err != nil {
		t.Fatal(err)
	}
	var flat map[string]any
	json.Unmarshal([]byte(subsetJSON), &flat)
	for leaf, disposition := range policy {
		if disposition == "" {
			if _, ok := flat[leaf]; !ok {
				t.Errorf("leaf %q is policy-include but MISSING from the subset map", leaf)
			}
		}
	}
}

// TestCompileKey_CanonicalFuzz: semantically identical configs built from
// differently-ordered YAML produce identical keys (spec test 4 config half).
func TestCompileKey_CanonicalFuzz(t *testing.T) {
	yamlA := []byte(`
version: 1
project: fuzz
output: wiki
sources:
  - path: raw
api:
  provider: openai
models:
  summarize: gpt-4o-mini
compiler:
  dedup_threshold: 0.9
  summary_max_tokens: 1000
`)
	yamlB := []byte(`
compiler:
  summary_max_tokens: 1000
  dedup_threshold: 0.9
models:
  summarize: gpt-4o-mini
api:
  provider: openai
output: wiki
project: fuzz
version: 1
sources:
  - path: raw
`)
	dir := t.TempDir()
	pA := dir + "/a.yaml"
	pB := dir + "/b.yaml"
	writeFileForKeyTest(t, pA, yamlA)
	writeFileForKeyTest(t, pB, yamlB)
	cfgA, err := config.Load(pA)
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := config.Load(pB)
	if err != nil {
		t.Fatal(err)
	}
	partsA, err2 := ComputeCompileKeyParts("sha256:z", 3, cfgA, nil)
	if err2 != nil {
		t.Fatal(err2)
	}
	kA := partsA.Key(3)
	partsB, err2 := ComputeCompileKeyParts("sha256:z", 3, cfgB, nil)
	if err2 != nil {
		t.Fatal(err2)
	}
	kB := partsB.Key(3)
	if kA != kB {
		t.Errorf("reordered YAML produced different keys:\nA %s\nB %s", kA, kB)
	}
}

func TestCompileKey_InjectedSubsetExactDelta(t *testing.T) {
	cfg := keyTestConfig()
	base := compileConfigSubset(cfg)
	injected := compileConfigSubsetForMode(cfg, true)
	removed := map[string]bool{
		"api.provider":          true,
		"api.extra_params":      true,
		"compiler.temperature":  true,
		"compiler.mode":         true,
		"compiler.prompt_cache": true,
	}
	for key, value := range base {
		got, ok := injected[key]
		if removed[key] {
			if ok {
				t.Errorf("injected subset retained %q", key)
			}
			continue
		}
		if !ok || !reflect.DeepEqual(got, value) {
			t.Errorf("injected subset changed %q: got=%#v present=%v want=%#v", key, got, ok, value)
		}
	}
	if got := injected["completion.mode"]; got != "injected" {
		t.Errorf("completion.mode = %#v, want injected", got)
	}
	if len(injected) != len(base)-len(removed)+1 {
		t.Errorf("subset sizes base=%d injected=%d, want exact five removals plus sentinel", len(base), len(injected))
	}
}

func TestCompileKey_InjectedModeBehavior(t *testing.T) {
	cfg := keyTestConfig()
	configTier3, err := ComputeCompileKeyParts("sha256:mode", 3, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	injectedTier3, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, cfg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if configTier3.Key(3) == injectedTier3.Key(3) {
		t.Fatal("config and injected Tier-3 keys are equal")
	}
	if got := DriftClass(configTier3, injectedTier3); got != "config" {
		t.Errorf("mode switch drift = %q, want config", got)
	}

	configTier1, err := ComputeCompileKeyParts("sha256:mode", 1, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	injectedTier1, err := ComputeCompileKeyPartsForMode("sha256:mode", 1, cfg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if configTier1.Key(1) != injectedTier1.Key(1) {
		t.Fatal("injected mode changed Tier-1 key")
	}

	changed := *cfg
	changed.API = cfg.API
	changed.Compiler = cfg.Compiler
	temperature := 0.9
	promptCache := false
	changed.API.ExtraParams = map[string]interface{}{"reasoning_effort": "high"}
	changed.Compiler.Temperature = &temperature
	changed.Compiler.Mode = "auto"
	changed.Compiler.PromptCache = &promptCache
	ignoredChanges, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, &changed, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if injectedTier3.Key(3) != ignoredChanges.Key(3) {
		t.Fatal("completion-unused config fields changed injected key")
	}

	providerChange := changed
	providerChange.API = changed.API
	providerChange.API.Provider = "anthropic"
	providerParts, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, &providerChange, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if injectedTier3.Config != providerParts.Config {
		t.Fatal("api.provider changed injected completion Config component")
	}
	if injectedTier3.Embed == providerParts.Embed || injectedTier3.Key(3) == providerParts.Key(3) {
		t.Fatal("api.provider fallback did not change the separate Embed component")
	}

	withEmbed := *cfg
	withEmbed.API = cfg.API
	withEmbed.Embed = &config.EmbedConfig{Provider: "openai", Model: "text-embedding-3-small", Dimensions: 4}
	withEmbedProviderChange := withEmbed
	withEmbedProviderChange.API = withEmbed.API
	withEmbedProviderChange.API.Provider = "anthropic"
	explicitEmbedA, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, &withEmbed, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	explicitEmbedB, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, &withEmbedProviderChange, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if explicitEmbedA.Key(3) != explicitEmbedB.Key(3) {
		t.Fatal("api.provider rekeyed injected mode despite an explicit embed.provider")
	}

	modelChangeCfg := *cfg
	modelChangeCfg.Models = cfg.Models
	modelChangeCfg.Models.Summarize = "different-model"
	modelChange, err := ComputeCompileKeyPartsForMode("sha256:mode", 3, &modelChangeCfg, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if injectedTier3.Key(3) == modelChange.Key(3) {
		t.Fatal("model change did not change injected key")
	}
}

// TestBuildFrontmatter_CanonicalProperty is spec test 4's frontmatter half:
// shuffled custom-field input order → identical bytes (the alias/source
// shuffle property lives in determinism_order_test.go, Task 3).
func TestBuildFrontmatter_CanonicalProperty(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000") // pin created_at
	c := ExtractedConcept{Name: "alpha", Sources: []string{"raw/a.md"}}
	order := []string{"field_b", "field_a", "field_c"}
	fieldsA := map[string]string{"field_a": "1", "field_b": "2", "field_c": "3"}
	fieldsB := map[string]string{"field_c": "3", "field_a": "1", "field_b": "2"}
	fmA := buildFrontmatter(c, "concept", fieldsA, order, time.UTC)
	fmB := buildFrontmatter(c, "concept", fieldsB, order, time.UTC)
	if fmA != fmB {
		t.Errorf("map-ordered field input changed frontmatter bytes:\n%s\nvs\n%s", fmA, fmB)
	}
}

func writeFileForKeyTest(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
