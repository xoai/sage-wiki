package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	publicprovider "github.com/xoai/sage-wiki/pkg/provider"
)

type injectedTestProvider struct {
	request  publicprovider.CompleteRequest
	response *publicprovider.CompleteResponse
	err      error
	wait     bool
}

func (p *injectedTestProvider) Complete(ctx context.Context, req publicprovider.CompleteRequest) (*publicprovider.CompleteResponse, error) {
	p.request = req
	if p.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.response, p.err
}

func (p *injectedTestProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func (p *injectedTestProvider) Models(context.Context) ([]publicprovider.ModelInfo, error) {
	return nil, nil
}

func TestInjectedClientMapsCompletionAndUsage(t *testing.T) {
	p := &injectedTestProvider{response: &publicprovider.CompleteResponse{
		Content: "<think>private reasoning</think>visible answer",
		Usage: publicprovider.Usage{
			InputTokens: 20, CachedTokens: 7, CacheWriteTokens: 3, OutputTokens: 5,
		},
	}}
	client, err := NewClientWithProvider(p)
	if err != nil {
		t.Fatalf("NewClientWithProvider: %v", err)
	}
	recorder := &captureRecorder{}
	tracker := mustCostTracker(t, "injected", 0)
	client.SetRecorder(recorder)
	client.SetTracker(tracker)
	client.SetPass("summarize")
	client.SetTier(3)

	response, err := client.ChatCompletionCtx(context.Background(), []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user prompt"},
	}, CallOpts{Model: "requested-model", MaxTokens: 321})
	if err != nil {
		t.Fatalf("ChatCompletionCtx: %v", err)
	}
	if response.Content != "visible answer" || response.Model != "requested-model" {
		t.Errorf("response = %+v, want stripped content and requested model fallback", response)
	}
	if p.request.Model != "requested-model" || p.request.MaxTokens != 321 || p.request.Tier != 3 {
		t.Errorf("request = %+v, want model/max/tier mapping", p.request)
	}
	if len(p.request.Messages) != 2 || p.request.Messages[0].Role != "system" ||
		p.request.Messages[1].Content != "user prompt" {
		t.Errorf("messages = %+v, want role/content mapping", p.request.Messages)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("usage events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Provider != "injected" || event.Model != "requested-model" ||
		event.Pass != "summarize" || event.Tier != 3 || event.InputTokens != 20 ||
		event.CachedTokens != 7 || event.CacheWriteTokens != 3 || event.OutputTokens != 5 {
		t.Errorf("usage event = %+v", event)
	}
	if event.Cost != nil {
		t.Errorf("unknown injected cost = %v, want nil", event.Cost)
	}
}

func TestInjectedClientPreservesErrorAndRejectsNilResponse(t *testing.T) {
	sentinel := errors.New("provider failed")
	tests := []struct {
		name string
		p    *injectedTestProvider
		want error
	}{
		{name: "provider error", p: &injectedTestProvider{err: sentinel}, want: sentinel},
		{name: "nil response", p: &injectedTestProvider{}, want: ErrInjectedNilResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewClientWithProvider(test.p)
			if err != nil {
				t.Fatalf("NewClientWithProvider: %v", err)
			}
			_, err = client.ChatCompletionCtx(context.Background(), nil, CallOpts{Model: "m"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestInjectedClientHonorsCallTimeout(t *testing.T) {
	p := &injectedTestProvider{wait: true}
	client, err := NewClientWithProvider(p)
	if err != nil {
		t.Fatalf("NewClientWithProvider: %v", err)
	}
	client.SetCallTimeout(20 * time.Millisecond)
	started := time.Now()
	_, err = client.ChatCompletionCtx(context.Background(), nil, CallOpts{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("call timeout took %s", elapsed)
	}
}

func TestInjectedClientCapabilitiesAndStructuredFallback(t *testing.T) {
	p := &injectedTestProvider{response: &publicprovider.CompleteResponse{
		Content: `[{"id":1}]`, Model: "injected-test-model",
	}}
	client, err := NewClientWithProvider(p)
	if err != nil {
		t.Fatalf("NewClientWithProvider: %v", err)
	}
	if client.ProviderName() != "injected" || client.SupportsVision() || client.SupportsBatch() {
		t.Fatalf("identity/capabilities = %q vision=%v batch=%v",
			client.ProviderName(), client.SupportsVision(), client.SupportsBatch())
	}
	if cacheID, err := client.SetupCache("system", "model"); err != nil || cacheID != "" {
		t.Fatalf("SetupCache = %q, %v, want no-op", cacheID, err)
	}
	if _, err := client.SubmitBatch(nil); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("SubmitBatch error = %v, want unsupported", err)
	}
	schema := JSONSchema{Name: "injected", IsArray: true, Schema: map[string]any{
		"type": "array", "minItems": 1,
		"items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
			"required":   []string{"id"},
		},
	}}
	payload, _, err := client.StructuredCompletion(context.Background(),
		[]Message{{Role: "user", Content: "return json"}}, schema, CallOpts{Model: "m"})
	if err != nil {
		t.Fatalf("StructuredCompletion: %v", err)
	}
	if string(payload) != `[{"id":1}]` {
		t.Errorf("payload = %s", payload)
	}
}

func TestInjectedClientPricingOverrideAndIdentityScopedTable(t *testing.T) {
	usage := publicprovider.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	newClient := func(t *testing.T, tracker *CostTracker) (*Client, *captureRecorder) {
		t.Helper()
		p := &injectedTestProvider{response: &publicprovider.CompleteResponse{
			Content: "ok", Model: "priced-model", Usage: usage,
		}}
		client, err := NewClientWithProvider(p)
		if err != nil {
			t.Fatal(err)
		}
		recorder := &captureRecorder{}
		client.SetTracker(tracker)
		client.SetRecorder(recorder)
		return client, recorder
	}

	t.Run("flat override", func(t *testing.T) {
		client, recorder := newClient(t, mustCostTracker(t, "injected", 2.5))
		if _, err := client.ChatCompletionCtx(context.Background(), nil, CallOpts{}); err != nil {
			t.Fatal(err)
		}
		if len(recorder.events) != 1 || recorder.events[0].Cost == nil {
			t.Fatalf("events = %+v, want priced override", recorder.events)
		}
	})

	t.Run("identity table", func(t *testing.T) {
		path := writeTable(t, `{"injected":{"priced-model":{"input":1,"output":2}}}`)
		client, recorder := newClient(t, trackerFromTable(t, "injected", 0, path))
		if _, err := client.ChatCompletionCtx(context.Background(), nil, CallOpts{}); err != nil {
			t.Fatal(err)
		}
		if len(recorder.events) != 1 || recorder.events[0].Cost == nil ||
			recorder.events[0].Cost.String() != "3" {
			t.Fatalf("events = %+v, want injected table cost 3", recorder.events)
		}
		other := trackerFromTable(t, "openai", 0, path)
		if _, ok := other.priceFor("priced-model"); ok {
			t.Fatal("injected table entry leaked into openai identity")
		}
	})
}
