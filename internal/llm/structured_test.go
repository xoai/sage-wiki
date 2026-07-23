package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type structuredStubProvider struct {
	mechanismOK bool
	body        []byte
	calls       *int32
	url         string
}

func (p structuredStubProvider) Name() string { return "stub" }
func (p structuredStubProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return nil, nil
}
func (p structuredStubProvider) ParseResponse(body []byte) (*Response, error) {
	return &Response{Content: "plain", Model: "stub-model", Usage: Usage{InputTokens: 1, OutputTokens: 1}}, nil
}
func (p structuredStubProvider) SupportsVision() bool { return false }

func (p structuredStubProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	if !p.mechanismOK {
		return nil, false, nil
	}
	return func() (*http.Request, error) {
		atomic.AddInt32(p.calls, 1)
		return http.NewRequest("POST", p.url, nil)
	}, true, nil
}

func (p structuredStubProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	return p.body, nil
}

func newStructuredClient(p Provider, server *httptest.Server) *Client {
	return &Client{
		provider: p,
		client:   *server.Client(),
		limiter:  newRateLimiter(0),
		tracker:  NewCostTracker("stub", 0),
	}
}

func TestStructuredCompletionUnwrap(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()

	p := structuredStubProvider{mechanismOK: true, body: []byte(`{"items": [{"id": 1, "score": 0.5}]}`), calls: &calls, url: server.URL}
	c := newStructuredClient(p, server)
	schema := JSONSchema{
		Name: "test", IsArray: true,
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":    map[string]any{"type": "integer"},
					"score": map[string]any{"type": "number"},
				},
				"required": []string{"id", "score"},
			},
			"minItems": 1,
		},
	}
	payload, raw, err := c.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Error("rawText on native path")
	}
	if string(payload) != `[{"id": 1, "score": 0.5}]` {
		t.Errorf("unwrap failed: %s", payload)
	}
	if calls != 1 {
		t.Errorf("request built %d times, want 1", calls)
	}
}

func TestStructuredCompletionValidationErrorNoFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok": true}`))
	}))
	defer server.Close()
	var calls int32
	p := structuredStubProvider{mechanismOK: true, body: []byte(`{"items": []}`), calls: &calls, url: server.URL}
	c := newStructuredClient(p, server)
	schema := JSONSchema{Name: "t", IsArray: true, Schema: map[string]any{
		"type": "array", "minItems": 1,
		"items": map[string]any{"type": "object", "properties": map[string]any{
			"id": map[string]any{"type": "integer"}}, "required": []string{"id"}},
	}}
	_, _, err := c.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "m"})
	if err == nil {
		t.Fatal("expected validation error (minItems), got nil — and it must NOT fall back")
	}
}

func TestStructuredCompletionDegradeChain(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`Invalid schema for response_format: minItems not supported`))
	}))
	defer server.Close()

	p := structuredStubProvider{mechanismOK: true, body: []byte(`{"items": []}`), calls: &calls, url: server.URL}
	c := newStructuredClient(p, server)
	schema := JSONSchema{Name: "t", IsArray: true, Schema: map[string]any{"type": "array", "minItems": 1,
		"items": map[string]any{"type": "object"}}}
	_, _, err := c.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	// degrade: 400-with-field-mention → json_object retry → also 400 →
	// surface the error (no fence-strip in this stub flow since retry failed)
	if calls < 2 {
		t.Errorf("expected initial + degrade request builds, got %d", calls)
	}
}

func TestStructuredCompletionNoDegradeOnContextLength(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`This model's maximum context length is 8192 tokens`))
	}))
	defer server.Close()
	p := structuredStubProvider{mechanismOK: true, body: []byte(`{}`), calls: &calls, url: server.URL}
	c := newStructuredClient(p, server)
	schema := JSONSchema{Name: "t", Schema: map[string]any{"type": "object"}}
	_, _, err := c.StructuredCompletion(t.Context(), []Message{{Role: "user", Content: "x"}}, schema, CallOpts{Model: "m"})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("context-length 400 triggered a degrade retry (calls=%d)", calls)
	}
}
