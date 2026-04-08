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
	// Verify both providers implement BatchProvider
	var _ BatchProvider = &anthropicProvider{}
	var _ BatchProvider = &openaiProvider{}
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

func TestClientBatchNotSupported(t *testing.T) {
	// Gemini doesn't support batch — should return error
	p := newGeminiProvider("key", "http://localhost")
	client := &Client{provider: p}

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
