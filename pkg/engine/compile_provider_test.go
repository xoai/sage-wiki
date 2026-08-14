package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/xoai/sage-wiki/pkg/events"
	"github.com/xoai/sage-wiki/pkg/provider"
	"github.com/xoai/sage-wiki/pkg/provider/providerfake"
)

type compileProviderScript struct {
	mu          sync.Mutex
	calls       []provider.CompleteRequest
	completeErr error
	nilResponse bool
	usage       provider.Usage
}

func (p *compileProviderScript) Complete(_ context.Context, req provider.CompleteRequest) (*provider.CompleteResponse, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req)
	p.mu.Unlock()
	if p.completeErr != nil {
		return nil, p.completeErr
	}
	if p.nilResponse {
		return nil, nil
	}
	return &provider.CompleteResponse{
		Content: compileProviderResponse(req.Messages),
		Model:   "injected-test-model",
		Usage:   p.usage,
	}, nil
}

func (p *compileProviderScript) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3, 0.4}
	}
	return out, nil
}

func (p *compileProviderScript) Models(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "injected-test-model", Family: "test"}}, nil
}

func (p *compileProviderScript) recordedCalls() []provider.CompleteRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.CompleteRequest, len(p.calls))
	copy(out, p.calls)
	return out
}

func compileProviderResponse(messages []provider.Message) string {
	var all strings.Builder
	for _, message := range messages {
		all.WriteString(strings.ToLower(message.Content))
		all.WriteByte(' ')
	}
	text := all.String()
	switch {
	case strings.Contains(text, "concept extraction system"):
		return `[{"name":"injected-concept","aliases":[],"sources":["raw/article.md"],"type":"concept"}]`
	case strings.Contains(text, "wiki author writing") || strings.Contains(text, "knowledge base article writer"):
		return "---\nconcept: injected-concept\n---\n\n# Injected Concept\n\n" +
			"This article is long enough to prove that the caller-owned completion provider " +
			"drives the full Tier-3 compiler without a credential in workspace configuration."
	default:
		return "## Key claims\n\nThe caller-owned provider supplies secretless compile completions.\n\n" +
			"## Concepts\n\ninjected-concept: A compile concept produced without persisted credentials."
	}
}

func writeCompileProviderFixture(t *testing.T, mode, auth, baseURL, apiKey string) string {
	t.Helper()
	dir := initWorkspace(t)
	var api strings.Builder
	api.WriteString("api:\n  provider: openai\n")
	if auth != "" {
		api.WriteString("  auth: " + auth + "\n")
	}
	if apiKey != "" {
		api.WriteString("  api_key: " + apiKey + "\n")
	}
	if baseURL != "" {
		api.WriteString("  base_url: " + baseURL + "\n")
	}
	cfg := `version: 1
project: compile-provider-test
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
` + api.String() + `models:
  summarize: injected-test-model
  extract: injected-test-model
  write: injected-test-model
compiler:
  auto_commit: false
  default_tier: 3
  mode: ` + mode + `
  prompt_cache: false
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "article.md"),
		[]byte("# Secretless compilation\n\nProvider credentials remain in memory while the compiler writes an article."), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return dir
}

func configCompletionStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if body["input"] != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0}},
			})
			return
		}
		calls.Add(1)
		messages, _ := body["messages"].([]any)
		publicMessages := make([]provider.Message, 0, len(messages))
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			publicMessages = append(publicMessages, provider.Message{
				Role:    stringValue(message["role"]),
				Content: stringValue(message["content"]),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{
				"role": "assistant", "content": compileProviderResponse(publicMessages),
			}}},
			"model": "injected-test-model",
			"usage": map[string]int{
				"prompt_tokens": 11, "completion_tokens": 5, "total_tokens": 16,
			},
		})
	}))
	return srv, &calls
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func enableConfigEmbeddings(t *testing.T, dir, baseURL string) {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	embed := "embed:\n" +
		"  provider: openai\n" +
		"  model: text-embedding-3-small\n" +
		"  dimensions: 4\n" +
		"  api_key: TEST_EMBED_CREDENTIAL\n" +
		"  base_url: " + baseURL + "\n"
	data = []byte(strings.Replace(string(data), "models:\n", embed+"models:\n", 1))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func enableInjectedPricing(t *testing.T, dir string) {
	t.Helper()
	pricePath := filepath.Join(dir, "injected-prices.json")
	prices := `{"injected":{"injected-test-model":{"input":1000,"cached_input":1000,"output":1000}}}`
	if err := os.WriteFile(pricePath, []byte(prices), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "  prompt_cache: false\n",
		"  prompt_cache: false\n  price_table: "+pricePath+"\n", 1))
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func forbiddenConfigStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil && body["input"] != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0}},
			})
			return
		}
		calls.Add(1)
		http.Error(w, "config completion path must not run", http.StatusInternalServerError)
	}))
	return srv, &calls
}

func TestCompileProvider_SecretlessTier3UsesInjectedOnly(t *testing.T) {
	configStub, configCalls := forbiddenConfigStub(t)
	defer configStub.Close()
	dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "")
	usage := provider.Usage{InputTokens: 11, CachedTokens: 3, CacheWriteTokens: 2, OutputTokens: 5}
	injected := &compileProviderScript{usage: usage}
	sink := &captureSink{}
	w, err := Open(context.Background(), dir,
		WithProvider(injected),
		WithCompileProvider(injected),
		WithEventSink(sink))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()

	maxCost := decimal.RequireFromString("0.000000000001")
	result, err := w.Compile(context.Background(), CompileRequest{
		Selector: "pending",
		Tier:     3,
		MaxCost:  &maxCost,
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.ArticlesWritten == 0 || result.ConceptsExtracted == 0 {
		t.Fatalf("result = %+v, want structured extraction and an article", result)
	}
	if configCalls.Load() != 0 {
		t.Fatalf("config completion endpoint called %d times", configCalls.Load())
	}
	calls := injected.recordedCalls()
	if len(calls) == 0 {
		t.Fatal("injected provider received no completion calls")
	}
	for i, call := range calls {
		if call.Tier != 3 || call.Model != "injected-test-model" || call.MaxTokens <= 0 {
			t.Errorf("call[%d] = %+v, want Tier=3, model, and positive max tokens", i, call)
		}
	}

	sink.mu.Lock()
	sinkEvents := append([]events.Event(nil), sink.events...)
	sink.mu.Unlock()
	var found bool
	for _, event := range sinkEvents {
		if event.Type != events.TypeUsage {
			continue
		}
		got := event.Data.(events.Usage)
		if got.Provider == "injected" && got.Model == "injected-test-model" {
			found = true
			if got.Pass == "" || got.Tier != 3 || got.InputTokens != usage.InputTokens ||
				got.CachedTokens != usage.CachedTokens || got.CacheWriteTokens != usage.CacheWriteTokens ||
				got.OutputTokens != usage.OutputTokens {
				t.Errorf("usage event = %+v, want full injected usage", got)
			}
			if got.Cost != nil {
				t.Errorf("usage cost = %v, want nil for unknown injected model", got.Cost)
			}
		}
	}
	if !found {
		t.Fatalf("no injected usage event in %+v", sinkEvents)
	}
}

func TestCompileProvider_ExplainUsesCompletionMode(t *testing.T) {
	configStub, configCalls := forbiddenConfigStub(t)
	defer configStub.Close()
	dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "")
	enableConfigEmbeddings(t, dir, configStub.URL)
	injected := &compileProviderScript{}
	w, err := Open(context.Background(), dir, WithCompileProvider(injected))
	if err != nil {
		t.Fatalf("Open injected workspace: %v", err)
	}
	result, err := w.Compile(context.Background(), CompileRequest{Tier: 3})
	if err != nil || result.ArticlesWritten == 0 {
		t.Fatalf("Compile result=%+v err=%v", result, err)
	}
	if configCalls.Load() != 0 {
		t.Fatalf("config completion endpoint called %d times", configCalls.Load())
	}
	explanation, err := w.ExplainCompile(context.Background(), "raw/article.md")
	if err != nil {
		t.Fatalf("ExplainCompile injected: %v", err)
	}
	if explanation.Verdict != "skip: unchanged" {
		t.Errorf("injected explanation verdict = %q, want skip: unchanged", explanation.Verdict)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close injected workspace: %v", err)
	}

	configWorkspace, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open config-backed workspace: %v", err)
	}
	defer configWorkspace.Close()
	explanation, err = configWorkspace.ExplainCompile(context.Background(), "raw/article.md")
	if err != nil {
		t.Fatalf("ExplainCompile config-backed: %v", err)
	}
	if explanation.Verdict != "compile: config" {
		t.Errorf("config explanation verdict = %q, want compile: config after mode switch", explanation.Verdict)
	}
}

func TestCompileProvider_PricedUsageTripsMaxCost(t *testing.T) {
	configStub, configCalls := forbiddenConfigStub(t)
	defer configStub.Close()
	dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "")
	enableInjectedPricing(t, dir)
	injected := &compileProviderScript{usage: provider.Usage{InputTokens: 11, OutputTokens: 5}}
	w, err := Open(context.Background(), dir,
		WithProvider(injected), WithCompileProvider(injected))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	maxCost := decimal.RequireFromString("0.000000000001")
	result, err := w.Compile(context.Background(), CompileRequest{Tier: 3, MaxCost: &maxCost})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Compile result=%+v err=%v, want ErrBudgetExceeded", result, err)
	}
	if len(injected.recordedCalls()) == 0 {
		t.Fatal("priced injected provider received no calls")
	}
	if configCalls.Load() != 0 {
		t.Fatalf("config completion endpoint called %d times", configCalls.Load())
	}
}

func TestWithProvider_RemainsSearchOnlyDuringTier3Compile(t *testing.T) {
	configStub, configCalls := configCompletionStub(t)
	defer configStub.Close()
	dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "TEST_CONFIG_CREDENTIAL")
	searchProvider := providerfake.New("completion path must remain config-backed")
	w, err := Open(context.Background(), dir, WithProvider(searchProvider))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	result, err := w.Compile(context.Background(), CompileRequest{Selector: "pending", Tier: 3})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.ArticlesWritten == 0 || configCalls.Load() == 0 {
		t.Fatalf("result=%+v config calls=%d, want config-backed article", result, configCalls.Load())
	}
	if calls := searchProvider.SortedCalls(); len(calls) != 0 {
		t.Fatalf("WithProvider completion calls = %d, want 0", len(calls))
	}
}

func TestCompileProvider_ErrorsNeverFallbackToConfig(t *testing.T) {
	tests := []struct {
		name     string
		provider *compileProviderScript
	}{
		{name: "error", provider: &compileProviderScript{completeErr: errors.New("injected completion failed")}},
		{name: "nil response", provider: &compileProviderScript{nilResponse: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configStub, configCalls := forbiddenConfigStub(t)
			defer configStub.Close()
			dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "TEST_CONFIG_CREDENTIAL")
			w, err := Open(context.Background(), dir,
				WithProvider(test.provider), WithCompileProvider(test.provider))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer w.Close()
			result, compileErr := w.Compile(context.Background(), CompileRequest{Selector: "pending", Tier: 3})
			if compileErr == nil && (result == nil || result.Errors == 0) {
				t.Fatalf("Compile result=%+v err=%v, want surfaced injected failure", result, compileErr)
			}
			if configCalls.Load() != 0 {
				t.Fatalf("config completion endpoint called %d times after injected failure", configCalls.Load())
			}
		})
	}
}

func TestCompileProvider_BatchModesAndAuto(t *testing.T) {
	tests := []struct {
		name string
		mode string
		auth string
		req  CompileRequest
	}{
		{name: "explicit", mode: "standard", req: CompileRequest{Batch: true}},
		{name: "config", mode: "batch"},
		{name: "subscription config", mode: "batch", auth: "subscription"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := writeCompileProviderFixture(t, test.mode, test.auth, "", "")
			injected := &compileProviderScript{}
			w, err := Open(context.Background(), dir,
				WithProvider(injected), WithCompileProvider(injected))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer w.Close()
			_, err = w.Compile(context.Background(), test.req)
			if err == nil || !strings.Contains(err.Error(), "injected") || !strings.Contains(err.Error(), "batch") {
				t.Fatalf("Compile error = %v, want injected batch error", err)
			}
		})
	}

	t.Run("auto stays standard", func(t *testing.T) {
		dir := writeCompileProviderFixture(t, "auto", "", "", "")
		injected := &compileProviderScript{}
		w, err := Open(context.Background(), dir,
			WithProvider(injected), WithCompileProvider(injected))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer w.Close()
		result, err := w.Compile(context.Background(), CompileRequest{Tier: 3})
		if err != nil || result.ArticlesWritten == 0 {
			t.Fatalf("Compile result=%+v err=%v, want synchronous article", result, err)
		}
	})
}

func TestCompileProvider_NilValuesUseConfig(t *testing.T) {
	var typedNil *compileProviderScript
	tests := []struct {
		name string
		opt  Option
	}{
		{name: "nil", opt: WithCompileProvider(nil)},
		{name: "typed nil", opt: WithCompileProvider(typedNil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configStub, configCalls := configCompletionStub(t)
			defer configStub.Close()
			dir := writeCompileProviderFixture(t, "standard", "", configStub.URL, "TEST_CONFIG_CREDENTIAL")
			w, err := Open(context.Background(), dir, test.opt)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer w.Close()
			result, err := w.Compile(context.Background(), CompileRequest{Tier: 3})
			if err != nil || result.ArticlesWritten == 0 || configCalls.Load() == 0 {
				t.Fatalf("Compile result=%+v err=%v config calls=%d, want config-backed equivalence",
					result, err, configCalls.Load())
			}
		})
	}
}
