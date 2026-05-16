package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- BatchProvider interface tests ---

func TestBatchProviderInterface(t *testing.T) {
	// Verify all three providers implement BatchProvider
	var _ BatchProvider = &anthropicProvider{}
	var _ BatchProvider = &openaiProvider{}
	var _ BatchProvider = &geminiProvider{}
}

// --- Anthropic batch tests ---

func TestAnthropicBatchSubmit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/batches" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Error("missing api key")
		}

		var body struct {
			Requests []json.RawMessage `json:"requests"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Requests) != 2 {
			t.Errorf("expected 2 requests, got %d", len(body.Requests))
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_abc123",
			"processing_status": "in_progress",
		})
	}))
	defer srv.Close()

	p := newAnthropicProvider("test-key", srv.URL)
	requests := []BatchRequest{
		{CustomID: "src-1", Messages: []Message{{Role: "user", Content: "summarize this"}}, Opts: CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 1024}},
		{CustomID: "src-2", Messages: []Message{{Role: "user", Content: "summarize that"}}, Opts: CallOpts{Model: "claude-sonnet-4-20250514", MaxTokens: 1024}},
	}

	batchID, err := p.SubmitBatch(requests)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if batchID != "batch_abc123" {
		t.Errorf("expected batch_abc123, got %s", batchID)
	}
}

func TestAnthropicBatchPoll(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/batches/batch_abc123" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_abc123",
			"processing_status": "ended",
			"results_url":      srvURL + "/v1/messages/batches/batch_abc123/results",
		})
	}))
	defer srv.Close()
	srvURL = srv.URL

	p := newAnthropicProvider("test-key", srv.URL)
	status, err := p.PollBatch("batch_abc123")
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	if status.Status != BatchEnded {
		t.Errorf("expected ended, got %s", status.Status)
	}
}

func TestAnthropicBatchRetrieve(t *testing.T) {
	// JSONL results — one success, one error
	lines := []string{
		`{"custom_id":"src-1","result":{"type":"succeeded","message":{"content":[{"type":"text","text":"Summary 1"}],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":100,"output_tokens":50}}}}`,
		`{"custom_id":"src-2","result":{"type":"errored","error":{"type":"server_error","message":"overloaded"}}}`,
	}

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/results") {
			w.Header().Set("Content-Type", "application/x-jsonlines")
			fmt.Fprintln(w, strings.Join(lines, "\n"))
			return
		}
		// Poll endpoint
		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_abc123",
			"processing_status": "ended",
			"results_url":      srvURL + "/v1/messages/batches/batch_abc123/results",
		})
	}))
	defer srv.Close()
	srvURL = srv.URL

	p := newAnthropicProvider("test-key", srv.URL)

	// First poll to get results URL
	status, _ := p.PollBatch("batch_abc123")
	results, err := p.RetrieveBatch(status.ResultsURL)
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Check success
	if results[0].CustomID != "src-1" || results[0].Error != "" {
		t.Errorf("expected success for src-1, got error: %s", results[0].Error)
	}
	if results[0].Response == nil || results[0].Response.Content != "Summary 1" {
		t.Error("expected response content for src-1")
	}

	// Check failure
	if results[1].CustomID != "src-2" || results[1].Error == "" {
		t.Error("expected error for src-2")
	}
}

// --- OpenAI batch tests ---

func TestOpenAIBatchSubmit(t *testing.T) {
	var mu sync.Mutex
	var uploadedFileID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.URL.Path == "/v1/files" && r.Method == "POST":
			// File upload
			json.NewEncoder(w).Encode(map[string]any{
				"id":       "file-upload-123",
				"filename": "batch_input.jsonl",
			})
			uploadedFileID = "file-upload-123"

		case r.URL.Path == "/v1/batches" && r.Method == "POST":
			var body struct {
				InputFileID string `json:"input_file_id"`
				Endpoint    string `json:"endpoint"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.InputFileID != uploadedFileID {
				t.Errorf("expected file ID %s, got %s", uploadedFileID, body.InputFileID)
			}
			if body.Endpoint != "/v1/chat/completions" {
				t.Errorf("unexpected endpoint: %s", body.Endpoint)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id":     "batch_xyz789",
				"status": "validating",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	p := newOpenAIProvider("test-key", srv.URL+"/v1")
	requests := []BatchRequest{
		{CustomID: "src-1", Messages: []Message{{Role: "user", Content: "summarize"}}, Opts: CallOpts{Model: "gpt-4o-mini", MaxTokens: 1024}},
	}

	batchID, err := p.SubmitBatch(requests)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
	if batchID != "batch_xyz789" {
		t.Errorf("expected batch_xyz789, got %s", batchID)
	}
}

func TestOpenAIBatchPoll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":             "batch_xyz789",
			"status":         "completed",
			"output_file_id": "file-out-456",
		})
	}))
	defer srv.Close()

	p := newOpenAIProvider("test-key", srv.URL+"/v1")
	status, err := p.PollBatch("batch_xyz789")
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	if status.Status != BatchEnded {
		t.Errorf("expected ended, got %s", status.Status)
	}
	if status.ResultsURL != "file-out-456" {
		t.Errorf("expected file-out-456, got %s", status.ResultsURL)
	}
}

func TestOpenAIBatchRetrieve(t *testing.T) {
	lines := []string{
		`{"custom_id":"src-1","response":{"status_code":200,"body":{"choices":[{"message":{"content":"Summary 1"}}],"model":"gpt-4o-mini","usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}}}`,
		`{"custom_id":"src-2","response":{"status_code":500,"body":{"error":{"message":"server error"}}}}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// File content download
		w.Header().Set("Content-Type", "application/jsonl")
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}))
	defer srv.Close()

	p := newOpenAIProvider("test-key", srv.URL+"/v1")
	results, err := p.RetrieveBatch("file-out-456")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].CustomID != "src-1" || results[0].Error != "" {
		t.Errorf("expected success for src-1")
	}
	if results[0].Response == nil || results[0].Response.Content != "Summary 1" {
		t.Error("expected content for src-1")
	}

	if results[1].CustomID != "src-2" || results[1].Error == "" {
		t.Error("expected error for src-2")
	}
}

// --- Client-level batch tests ---

// noBatchProvider is a minimal Provider stub that does not implement BatchProvider.
type noBatchProvider struct{}

func (noBatchProvider) Name() string                                                    { return "nobatch" }
func (noBatchProvider) SupportsVision() bool                                            { return false }
func (noBatchProvider) FormatRequest([]Message, CallOpts) (*http.Request, error)        { return nil, nil }
func (noBatchProvider) ParseResponse([]byte) (*Response, error)                         { return nil, nil }

func TestClientBatchNotSupported(t *testing.T) {
	// A provider that doesn't implement BatchProvider should return an error.
	client := &Client{provider: noBatchProvider{}}

	_, err := client.SubmitBatch(nil)
	if err == nil {
		t.Error("expected error for non-batch provider")
	}
}

func TestBatchCostTracking(t *testing.T) {
	ct := NewCostTracker("anthropic", 0)

	// Batch mode should use batch pricing
	ct.Track("summarize", "claude-sonnet-4-20250514", Usage{InputTokens: 1000, OutputTokens: 200}, true)

	report := ct.Report()
	if report.EstimatedCost <= 0 {
		t.Error("expected positive cost")
	}

	// Compare with non-batch — batch should be cheaper
	ctNormal := NewCostTracker("anthropic", 0)
	ctNormal.Track("summarize", "claude-sonnet-4-20250514", Usage{InputTokens: 1000, OutputTokens: 200}, false)

	normalReport := ctNormal.Report()
	if report.EstimatedCost >= normalReport.EstimatedCost {
		t.Errorf("batch cost $%.6f should be less than normal $%.6f",
			report.EstimatedCost, normalReport.EstimatedCost)
	}
}

func TestAnthropicBatchExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id":                "batch_expired",
			"processing_status": "expired",
		})
	}))
	defer srv.Close()

	p := newAnthropicProvider("test-key", srv.URL)
	status, err := p.PollBatch("batch_expired")
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}
	if status.Status != BatchExpired {
		t.Errorf("expected expired, got %s", status.Status)
	}
}

// --- Gemini batch tests ---

func TestParseGeminiBatchResults(t *testing.T) {
	tests := []struct {
		name      string
		jsonl     string
		wantCount int
		check     func(t *testing.T, results []BatchResult)
	}{
		{
			name: "successful response",
			jsonl: `{"key":"req-1","response":{"candidates":[{"content":{"parts":[{"text":"hello world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-flash"}}`,
			wantCount: 1,
			check: func(t *testing.T, results []BatchResult) {
				r := results[0]
				if r.CustomID != "req-1" {
					t.Errorf("CustomID = %q, want %q", r.CustomID, "req-1")
				}
				if r.Error != "" {
					t.Errorf("unexpected error: %s", r.Error)
				}
				if r.Response == nil {
					t.Fatal("expected non-nil response")
				}
				if r.Response.Content != "hello world" {
					t.Errorf("Content = %q, want %q", r.Response.Content, "hello world")
				}
				if r.Response.Model != "gemini-2.5-flash" {
					t.Errorf("Model = %q, want %q", r.Response.Model, "gemini-2.5-flash")
				}
				if r.Response.TokensUsed != 15 {
					t.Errorf("TokensUsed = %d, want 15", r.Response.TokensUsed)
				}
				if r.Response.Usage.InputTokens != 10 {
					t.Errorf("InputTokens = %d, want 10", r.Response.Usage.InputTokens)
				}
				if r.Response.Usage.OutputTokens != 5 {
					t.Errorf("OutputTokens = %d, want 5", r.Response.Usage.OutputTokens)
				}
			},
		},
		{
			name:      "per-request error",
			jsonl:     `{"key":"req-2","status":{"code":429,"message":"quota exceeded"}}`,
			wantCount: 1,
			check: func(t *testing.T, results []BatchResult) {
				r := results[0]
				if r.CustomID != "req-2" {
					t.Errorf("CustomID = %q, want %q", r.CustomID, "req-2")
				}
				if r.Response != nil {
					t.Error("expected nil response on error")
				}
				if r.Error != "code 429: quota exceeded" {
					t.Errorf("Error = %q, want %q", r.Error, "code 429: quota exceeded")
				}
			},
		},
		{
			name:      "empty candidate list gives empty-response error",
			jsonl:     `{"key":"req-3","response":{"candidates":[]}}`,
			wantCount: 1,
			check: func(t *testing.T, results []BatchResult) {
				if results[0].Error != "empty response" {
					t.Errorf("Error = %q, want %q", results[0].Error, "empty response")
				}
			},
		},
		{
			name: "multiple entries mixed",
			jsonl: strings.Join([]string{
				`{"key":"a","response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{}}}`,
				`{"key":"b","status":{"code":500,"message":"internal error"}}`,
				``,
				`{"key":"c","response":{"candidates":[{"content":{"parts":[{"text":"also ok"}]},"finishReason":"STOP"}],"usageMetadata":{}}}`,
			}, "\n"),
			wantCount: 3,
			check: func(t *testing.T, results []BatchResult) {
				ids := []string{results[0].CustomID, results[1].CustomID, results[2].CustomID}
				if ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
					t.Errorf("unexpected IDs: %v", ids)
				}
				if results[0].Error != "" {
					t.Errorf("entry a: unexpected error %q", results[0].Error)
				}
				if results[1].Response != nil {
					t.Error("entry b: expected nil response")
				}
				if results[2].Response == nil || results[2].Response.Content != "also ok" {
					t.Errorf("entry c: unexpected response %v", results[2].Response)
				}
			},
		},
		{
			name:      "malformed line is skipped",
			jsonl:     "not-json\n{\"key\":\"good\",\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}],\"usageMetadata\":{}}}",
			wantCount: 1,
			check: func(t *testing.T, results []BatchResult) {
				if results[0].CustomID != "good" {
					t.Errorf("CustomID = %q, want %q", results[0].CustomID, "good")
				}
			},
		},
		{
			name:      "empty input",
			jsonl:     "",
			wantCount: 0,
			check:     func(t *testing.T, results []BatchResult) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := parseGeminiBatchResults(strings.NewReader(tt.jsonl))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(results) != tt.wantCount {
				t.Fatalf("got %d results, want %d", len(results), tt.wantCount)
			}
			tt.check(t, results)
		})
	}
}

func TestGeminiBatchState(t *testing.T) {
	tests := []struct {
		state string
		want  BatchStatus
	}{
		{"JOB_STATE_SUCCEEDED", BatchEnded},
		{"JOB_STATE_EXPIRED", BatchExpired},
		{"JOB_STATE_FAILED", BatchFailed},
		{"JOB_STATE_CANCELLED", BatchFailed},
		{"JOB_STATE_RUNNING", BatchInProgress},
		{"", BatchInProgress},
	}
	for _, tt := range tests {
		got := geminiBatchState(tt.state)
		if got != tt.want {
			t.Errorf("geminiBatchState(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestNewGeminiProviderURLDerivation(t *testing.T) {
	tests := []struct {
		baseURL         string
		wantUpload      string
		wantDownload    string
		wantBatchHost   string
	}{
		{
			baseURL:       "https://generativelanguage.googleapis.com/v1beta",
			wantUpload:    "https://generativelanguage.googleapis.com/upload/v1beta",
			wantDownload:  "https://generativelanguage.googleapis.com/download/v1beta",
			wantBatchHost: "generativelanguage.googleapis.com",
		},
		{
			baseURL:       "https://my-proxy.example.com/v1beta",
			wantUpload:    "https://my-proxy.example.com/upload/v1beta",
			wantDownload:  "https://my-proxy.example.com/download/v1beta",
			wantBatchHost: "my-proxy.example.com",
		},
	}
	for _, tt := range tests {
		p := newGeminiProvider("key", tt.baseURL)
		if p.uploadBaseURL != tt.wantUpload {
			t.Errorf("baseURL=%q: uploadBaseURL = %q, want %q", tt.baseURL, p.uploadBaseURL, tt.wantUpload)
		}
		if p.downloadBaseURL != tt.wantDownload {
			t.Errorf("baseURL=%q: downloadBaseURL = %q, want %q", tt.baseURL, p.downloadBaseURL, tt.wantDownload)
		}
		if p.batchHost != tt.wantBatchHost {
			t.Errorf("baseURL=%q: batchHost = %q, want %q", tt.baseURL, p.batchHost, tt.wantBatchHost)
		}
	}
}

// TestSupportsBatch_PerProvider verifies that only providers with a real
// Files/Batches API claim BatchProvider support. Ollama, Qwen, and other
// OpenAI-compatible chat backends share openaiProvider for chat completion
// but must NOT inherit its batch methods (issue #83).
func TestSupportsBatch_PerProvider(t *testing.T) {
	tests := []struct {
		provider    string
		wantSupport bool
	}{
		{"openai", true},
		{"anthropic", true},
		{"gemini", true},
		{"openai-compatible", false},
		{"qwen", false},
		{"ollama", false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			c, err := NewClient(tt.provider, "test-key", "", 0, nil)
			if err != nil {
				t.Fatalf("NewClient(%q): %v", tt.provider, err)
			}
			got := c.SupportsBatch()
			if got != tt.wantSupport {
				t.Errorf("%s SupportsBatch() = %v, want %v", tt.provider, got, tt.wantSupport)
			}
		})
	}
}

// TestNonBatchProvider_DoesNotSatisfyBatchInterface guards against a
// regression where someone changes nonBatchProvider from a named field
// to struct embedding (which would re-promote the batch methods).
func TestNonBatchProvider_DoesNotSatisfyBatchInterface(t *testing.T) {
	p := &nonBatchProvider{inner: newOpenAIProvider("k", "http://localhost:11434/v1")}
	if _, ok := interface{}(p).(BatchProvider); ok {
		t.Error("nonBatchProvider must NOT satisfy BatchProvider — issue #83 regression")
	}
}
