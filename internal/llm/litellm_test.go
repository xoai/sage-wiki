package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

var testAsOf = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

// answerSchema is an object-rooted schema matching the fake proxy's reply.
func answerSchema() JSONSchema {
	return JSONSchema{
		Name: "answer",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
			"required":   []string{"answer"},
		},
	}
}

// litellmModelInfo is a /model/info body shaped like a real proxy's: a
// load-balanced alias (two deployments), an alias whose name differs from
// its backend model, an embedding alias, and an alias with no known price.
const litellmModelInfo = `{"data": [
  {"model_name": "gpt-4.1-mini", "litellm_params": {"model": "azure/gpt-4.1-mini"},
   "model_info": {"mode": "chat", "input_cost_per_token": 4e-07, "output_cost_per_token": 1.6e-06,
                  "cache_read_input_token_cost": 1e-07, "supports_response_schema": true}},
  {"model_name": "gpt-4.1-mini", "litellm_params": {"model": "openai/gpt-4.1-mini"},
   "model_info": {"mode": "chat", "input_cost_per_token": 9e-07, "output_cost_per_token": 9e-06}},
  {"model_name": "fast", "litellm_params": {"model": "gemini/gemini-2.5-flash"},
   "model_info": {"mode": "chat", "input_cost_per_token": 3e-07, "output_cost_per_token": 2.5e-06,
                  "supports_response_schema": false}},
  {"model_name": "embed", "litellm_params": {"model": "azure/text-embedding-3-small"},
   "model_info": {"mode": "embedding", "input_cost_per_token": 2e-08, "output_vector_size": 1536}},
  {"model_name": "house-model", "litellm_params": {"model": "hosted_vllm/qwen3"},
   "model_info": {"mode": "chat"}},
  {"model_name": "free-tier", "litellm_params": {"model": "openai/gpt-5-nano"},
   "model_info": {"mode": "chat", "input_cost_per_token": 0.0, "output_cost_per_token": 0.0}},
  {"model_name": "paid-tier", "litellm_params": {"model": "openai/gpt-5-nano"},
   "model_info": {"mode": "chat", "input_cost_per_token": 5e-08, "output_cost_per_token": 4e-07}},
  {"model_name": "", "model_info": {"mode": "chat"}}
]}`

// fakeLiteLLMProxy serves /model/info and /chat/completions, recording the
// last chat request body and Authorization header.
type fakeLiteLLMProxy struct {
	*httptest.Server
	infoCalls atomic.Int32
	lastAuth  atomic.Value
	lastBody  atomic.Value
}

func newFakeLiteLLMProxy(t *testing.T, infoStatus int) *fakeLiteLLMProxy {
	t.Helper()
	f := &fakeLiteLLMProxy{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth.Store(r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/model/info":
			f.infoCalls.Add(1)
			w.WriteHeader(infoStatus)
			w.Write([]byte(litellmModelInfo))
		case "/chat/completions":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.lastBody.Store(body)
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": `{"answer":"4"}`}, "finish_reason": "stop"}},
				"model":   "gpt-4.1-mini-2025-04-14",
				"usage":   map[string]int{"prompt_tokens": 1000000, "completion_tokens": 1000000, "total_tokens": 2000000},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	resetLiteLLMModelCache(t)
	return f
}

func resetLiteLLMModelCache(t *testing.T) {
	t.Helper()
	clear := func() {
		litellmModelCacheMu.Lock()
		litellmModelCache = map[string]litellmModelCacheEntry{}
		litellmModelCacheMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// hermeticHome points the user price file lookup at an empty directory.
func hermeticHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestLiteLLMChatCompletion(t *testing.T) {
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	client, err := NewClient("litellm", "sk-proxy", proxy.URL, 0)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := client.ChatCompletion([]Message{{Role: "user", Content: "2+2?"}}, CallOpts{Model: "fast", MaxTokens: 64})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Content != `{"answer":"4"}` || resp.Usage.InputTokens != 1000000 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if got := proxy.lastAuth.Load(); got != "Bearer sk-proxy" {
		t.Errorf("Authorization = %v", got)
	}
	body := proxy.lastBody.Load().(map[string]any)
	if body["model"] != "fast" || body["max_tokens"] != float64(64) {
		t.Errorf("request body = %v", body)
	}
	if body["drop_params"] != true {
		t.Errorf("drop_params must default to true, got %v", body["drop_params"])
	}
	if client.ProviderName() != "litellm" {
		t.Errorf("ProviderName = %q", client.ProviderName())
	}

	// extra_params ride along and can opt out of drop_params.
	optOut, err := NewClient("litellm", "sk-proxy", proxy.URL, 0, map[string]interface{}{"drop_params": false, "reasoning_effort": "low"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := optOut.ChatCompletion([]Message{{Role: "user", Content: "hi"}}, CallOpts{Model: "fast"}); err != nil {
		t.Fatal(err)
	}
	body = proxy.lastBody.Load().(map[string]any)
	if body["drop_params"] != false || body["reasoning_effort"] != "low" {
		t.Errorf("extra_params not applied: %v", body)
	}
}

func TestLiteLLMProviderCapabilities(t *testing.T) {
	p, err := newProvider("litellm", "", "")
	if err != nil {
		t.Fatalf("newProvider: %v", err)
	}
	if _, ok := p.(BatchProvider); ok {
		t.Error("litellm must not claim BatchProvider (OpenAI Files/Batches are not routed per deployment)")
	}
	if _, ok := p.(StreamingProvider); !ok {
		t.Error("litellm must stream via the proxy's /chat/completions")
	}
	if _, ok := p.(extraParamsSetter); !ok {
		t.Error("litellm must accept extra_params")
	}
	if !degradesToJSONObject(p) {
		t.Error("litellm reuses the OpenAI formatter and must take the json_object degrade")
	}
	if got := p.(*litellmProvider).inner.baseURL; got != DefaultLiteLLMBaseURL {
		t.Errorf("default base URL = %q", got)
	}
	if got := newLiteLLMProvider("", "http://proxy:4000/").inner.baseURL; got != "http://proxy:4000" {
		t.Errorf("trailing slash not trimmed: %q", got)
	}
	if got := defaultRateLimit("litellm"); got != 0 {
		t.Errorf("defaultRateLimit(litellm) = %d, want 0 (the proxy enforces its own limits)", got)
	}
}

func TestLiteLLMModelsParsesModelInfo(t *testing.T) {
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	models, err := LiteLLMModels(proxy.URL+"/", "")
	if err != nil {
		t.Fatalf("LiteLLMModels: %v", err)
	}
	if len(models) != 7 {
		t.Fatalf("got %d models, want 7 (nameless entry skipped)", len(models))
	}
	if got := proxy.lastAuth.Load(); got != "" {
		t.Errorf("keyless proxy got Authorization %q", got)
	}
	fast := models[2]
	if fast.Name != "fast" || fast.Underlying != "gemini-2.5-flash" || fast.Mode != "chat" || fast.SupportsResponseSchema {
		t.Errorf("fast = %+v", fast)
	}
	if models[0].CacheReadCostPerToken == nil || *models[0].CacheReadCostPerToken != 1e-07 || models[0].CacheWriteCostPerToken != nil {
		t.Errorf("cache prices = %v / %v", models[0].CacheReadCostPerToken, models[0].CacheWriteCostPerToken)
	}

	if _, err := LiteLLMModels(proxy.URL, ""); err != nil {
		t.Fatal(err)
	}
	if n := proxy.infoCalls.Load(); n != 1 {
		t.Errorf("/model/info fetched %d times, want 1 (cached per proxy and key)", n)
	}
	if _, err := LiteLLMModels(proxy.URL, "sk-other"); err != nil {
		t.Fatal(err)
	}
	if n := proxy.infoCalls.Load(); n != 2 {
		t.Errorf("a different key must not share the cache entry (fetches = %d)", n)
	}

	m, ok := LiteLLMEmbeddingModel(proxy.URL, "")
	if !ok || m.Name != "embed" || m.OutputVectorSize != 1536 {
		t.Errorf("LiteLLMEmbeddingModel = %+v, %v", m, ok)
	}
}

func TestLiteLLMPrices(t *testing.T) {
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	models, err := LiteLLMModels(proxy.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	prices := litellmPrices(models, testAsOf)

	alias, ok := prices["litellm:gpt-4.1-mini"]
	if !ok {
		t.Fatal("alias not priced")
	}
	if alias.Source != PriceSourceProviderAPI {
		t.Errorf("source = %q", alias.Source)
	}
	// First deployment of a load-balanced alias wins; per-token → per-1M exact.
	if alias.InputPerMTok.String() != "0.4" || alias.OutputPerMTok.String() != "1.6" || alias.CachedInputPerMTok.String() != "0.1" {
		t.Errorf("alias price = %s / %s / %s", alias.InputPerMTok, alias.OutputPerMTok, alias.CachedInputPerMTok)
	}
	if alias.CacheWritePerMTok != nil || alias.BatchInputPerMTok != nil {
		t.Error("unreported components must stay nil (unknown), never zero")
	}
	// The backend model the response reports resolves to the alias price.
	if under, ok := prices["litellm:gemini-2.5-flash"]; !ok || under.InputPerMTok.String() != "0.3" {
		t.Errorf("underlying model not priced: %+v", under)
	}
	// Aliases that disagree on a shared backend's price leave it unknown.
	if _, ok := prices["litellm:gpt-5-nano"]; ok {
		t.Error("free and paid aliases over one deployment must not price the backend name")
	}
	if free := prices["litellm:free-tier"]; free.InputPerMTok == nil || !free.InputPerMTok.IsZero() {
		t.Errorf("a $0 alias is a known zero price, got %v", free.InputPerMTok)
	}
	if _, ok := prices["litellm:house-model"]; ok {
		t.Error("an alias without prices must stay unknown")
	}
	if _, ok := prices["litellm:embed"]; !ok {
		t.Error("embedding alias with an input price should be listed")
	}
}

func TestLoadRegistryForLiteLLMPrecedence(t *testing.T) {
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	dir := t.TempDir()
	userPath := filepath.Join(dir, "prices.json")
	os.WriteFile(userPath, []byte(`{"prices": {"litellm:fast": {"input": "9", "output": "9"}}}`), 0o644)

	ep := Endpoint{Provider: "litellm", BaseURL: proxy.URL}
	r, err := loadRegistryWith("", userPath, providerAPIPrices(ep))
	if err != nil {
		t.Fatal(err)
	}
	// Versioned backend model → prefix match on the underlying-name entry.
	p, ok := r.Lookup("litellm", "gpt-4.1-mini-2025-04-14")
	if !ok || p.InputPerMTok.String() != "0.4" || p.Source != PriceSourceProviderAPI {
		t.Errorf("versioned lookup = %+v, %v", p, ok)
	}
	// An explicit user price beats the proxy's.
	if p, _ := r.Lookup("litellm", "fast"); p == nil || p.InputPerMTok.String() != "9" || p.Source != PriceSourceUser {
		t.Errorf("user override lost: %+v", p)
	}
	// Builtin entries are untouched and never leak across providers.
	if _, ok := r.Lookup("litellm", "gpt-4o"); ok {
		t.Error("builtin openai:gpt-4o must not resolve under litellm")
	}
	if providerAPIPrices(Endpoint{Provider: "openai-compatible", BaseURL: proxy.URL}) != nil {
		t.Error("only litellm reports prices over its API")
	}
}

func TestLiteLLMCostTrackedThroughClient(t *testing.T) {
	hermeticHome(t)
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	client, err := NewClient("litellm", "sk-proxy", proxy.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	tracker, err := NewCostTrackerForEndpoint(client.PriceEndpoint(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	client.SetTracker(tracker)
	if _, err := client.ChatCompletion([]Message{{Role: "user", Content: "hi"}}, CallOpts{Model: "gpt-4.1-mini"}); err != nil {
		t.Fatal(err)
	}
	report := tracker.Report()
	// 1M input at $0.40 + 1M output at $1.60, priced via the response's
	// versioned backend model name.
	if report.Cost == nil || report.Cost.String() != "2" {
		t.Fatalf("total cost = %v (unknown: %v)", report.Cost, report.UnknownModels)
	}

	// Tracker-less clients (query/expansion paths) price through the same registry.
	ev := client.buildUsageEvent("gpt-4.1-mini-2025-04-14", Usage{InputTokens: 1000000})
	if ev.Cost == nil || ev.Cost.String() != "0.4" || ev.PriceSource != PriceSourceProviderAPI {
		t.Errorf("usage event = %v %q", ev.Cost, ev.PriceSource)
	}

	_, est, err := EstimateFromBytesFor(4000000, client.PriceEndpoint(), "fast", 0, "")
	if err != nil || est == nil {
		t.Fatalf("estimate = %v, %v", est, err)
	}
}

func TestLiteLLMModelInfoUnavailable(t *testing.T) {
	hermeticHome(t)
	proxy := newFakeLiteLLMProxy(t, http.StatusUnauthorized)
	ep := Endpoint{Provider: "litellm", BaseURL: proxy.URL, APIKey: "sk-virtual"}
	r, err := LoadRegistryFor("", ep)
	if err != nil {
		t.Fatalf("an unreachable /model/info must not fail registry loading: %v", err)
	}
	if _, ok := r.Lookup("litellm", "gpt-4.1-mini"); ok {
		t.Error("no proxy prices → unknown, never a guess")
	}
	p := newLiteLLMProvider("sk-virtual", proxy.URL)
	if _, ok, err := p.FormatStructuredRequest(nil, testSchema(), CallOpts{Model: "gpt-4.1-mini"}); ok || err != nil {
		t.Errorf("structured must fall back without model info (ok=%v err=%v)", ok, err)
	}
	if _, ok := LiteLLMEmbeddingModel(proxy.URL, "sk-virtual"); ok {
		t.Error("no embedding alias without model info")
	}
	if n := proxy.infoCalls.Load(); n != 1 {
		t.Errorf("failure should be cached briefly, fetched %d times", n)
	}
}

func TestLiteLLMStructuredGatedOnResponseSchema(t *testing.T) {
	proxy := newFakeLiteLLMProxy(t, http.StatusOK)
	p := newLiteLLMProvider("", proxy.URL)

	build, ok, err := p.FormatStructuredRequest([]Message{{Role: "user", Content: "2+2?"}}, testSchema(), CallOpts{Model: "gpt-4.1-mini"})
	if err != nil || !ok {
		t.Fatalf("supports_response_schema alias: ok=%v err=%v", ok, err)
	}
	req, err := build()
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	rf, _ := body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", body["response_format"])
	}

	for _, model := range []string{"fast", "house-model", "not-on-proxy"} {
		if _, ok, _ := p.FormatStructuredRequest(nil, testSchema(), CallOpts{Model: model}); ok {
			t.Errorf("%s: structured must fall back unless the proxy reports supports_response_schema", model)
		}
	}

	client, err := NewClient("litellm", "", proxy.URL, 0)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := client.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "2+2?"}}, answerSchema(), CallOpts{Model: "gpt-4.1-mini"})
	if err != nil {
		t.Fatalf("StructuredCompletion: %v", err)
	}
	if string(payload) != `{"answer":"4"}` {
		t.Errorf("payload = %s", payload)
	}
	sent := proxy.lastBody.Load().(map[string]any)
	if rf, _ := sent["response_format"].(map[string]any); rf["type"] != "json_schema" {
		t.Errorf("native structured path not taken: response_format = %v", sent["response_format"])
	}
}
