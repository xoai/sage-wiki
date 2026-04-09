package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
)

// validateBatchURL checks that a results URL points to an expected API host.
// This prevents SSRF if compile-state.json is tampered with, since the URL
// is fetched with API credentials attached.
func validateBatchURL(rawURL string, allowedHost string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid results URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("results URL has disallowed scheme %q", u.Scheme)
	}
	if u.Host != allowedHost {
		return fmt.Errorf("results URL host %q does not match expected %q", u.Host, allowedHost)
	}
	return nil
}

// BatchStatus represents the state of a batch job.
type BatchStatus string

const (
	BatchInProgress BatchStatus = "in_progress"
	BatchEnded      BatchStatus = "ended"
	BatchExpired    BatchStatus = "expired"
	BatchFailed     BatchStatus = "failed"
)

// BatchRequest is a single request within a batch submission.
type BatchRequest struct {
	CustomID string    // unique identifier (e.g. source path)
	Messages []Message // chat messages
	Opts     CallOpts  // model, max_tokens, temperature
}

// BatchStatusResponse holds the result of polling a batch.
type BatchStatusResponse struct {
	BatchID    string
	Status     BatchStatus
	ResultsURL string // Anthropic: results URL; OpenAI: output_file_id
}

// BatchResult is a single result from a completed batch.
type BatchResult struct {
	CustomID string
	Response *Response // nil if errored
	Error    string    // non-empty if this request failed
}

// BatchProvider extends Provider with async batch API support.
// Only Anthropic and OpenAI support batch; Gemini does not.
type BatchProvider interface {
	// SubmitBatch sends a batch of requests. Returns the batch ID.
	SubmitBatch(requests []BatchRequest) (batchID string, err error)

	// PollBatch checks the status of a batch job.
	PollBatch(batchID string) (*BatchStatusResponse, error)

	// RetrieveBatch downloads and parses batch results.
	// For Anthropic: resultsRef is the results URL.
	// For OpenAI: resultsRef is the output_file_id.
	RetrieveBatch(resultsRef string) ([]BatchResult, error)
}

// --- Client-level batch methods ---

// SubmitBatch submits a batch if the provider supports it.
func (c *Client) SubmitBatch(requests []BatchRequest) (string, error) {
	bp, ok := c.provider.(BatchProvider)
	if !ok {
		return "", fmt.Errorf("llm: provider %s does not support batch API", c.provider.Name())
	}
	return bp.SubmitBatch(requests)
}

// PollBatch checks batch status.
func (c *Client) PollBatch(batchID string) (*BatchStatusResponse, error) {
	bp, ok := c.provider.(BatchProvider)
	if !ok {
		return nil, fmt.Errorf("llm: provider %s does not support batch API", c.provider.Name())
	}
	return bp.PollBatch(batchID)
}

// RetrieveBatch downloads batch results.
func (c *Client) RetrieveBatch(resultsRef string) ([]BatchResult, error) {
	bp, ok := c.provider.(BatchProvider)
	if !ok {
		return nil, fmt.Errorf("llm: provider %s does not support batch API", c.provider.Name())
	}
	return bp.RetrieveBatch(resultsRef)
}

// SupportsBatch returns whether the current provider supports batch API.
func (c *Client) SupportsBatch() bool {
	_, ok := c.provider.(BatchProvider)
	return ok
}

// --- Anthropic BatchProvider ---

func (p *anthropicProvider) SubmitBatch(requests []BatchRequest) (string, error) {
	var batchRequests []map[string]any
	for _, r := range requests {
		body, _ := p.formatBody(r.Messages, r.Opts, false)
		batchRequests = append(batchRequests, map[string]any{
			"custom_id": r.CustomID,
			"params":    body,
		})
	}

	payload := map[string]any{
		"requests": batchRequests,
	}

	req, err := http.NewRequest("POST", p.baseURL+"/v1/messages/batches", jsonBody(payload))
	if err != nil {
		return "", fmt.Errorf("anthropic batch: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic batch: submit: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic batch: submit returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("anthropic batch: parse submit response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("anthropic batch: submit returned empty batch ID")
	}

	log.Info("anthropic batch submitted", "batch_id", result.ID, "requests", len(requests))
	return result.ID, nil
}

func (p *anthropicProvider) PollBatch(batchID string) (*BatchStatusResponse, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/v1/messages/batches/"+batchID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: poll: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic batch: poll returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID               string `json:"id"`
		ProcessingStatus string `json:"processing_status"`
		ResultsURL       string `json:"results_url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("anthropic batch: parse poll response: %w", err)
	}

	status := mapAnthropicBatchStatus(result.ProcessingStatus)
	return &BatchStatusResponse{
		BatchID:    result.ID,
		Status:     status,
		ResultsURL: result.ResultsURL,
	}, nil
}

func (p *anthropicProvider) RetrieveBatch(resultsURL string) ([]BatchResult, error) {
	// Validate URL to prevent SSRF — resultsURL may come from checkpoint file on disk.
	expectedHost := "api.anthropic.com"
	if p.baseURL != "" {
		if u, err := url.Parse(p.baseURL); err == nil && u.Host != "" {
			expectedHost = u.Host
		}
	}
	if err := validateBatchURL(resultsURL, expectedHost); err != nil {
		return nil, fmt.Errorf("anthropic batch: %w", err)
	}

	req, err := http.NewRequest("GET", resultsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic batch: retrieve: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic batch: retrieve returned %d: %s", resp.StatusCode, string(body))
	}

	return parseAnthropicBatchResults(resp.Body)
}

func parseAnthropicBatchResults(r io.Reader) ([]BatchResult, error) {
	var results []BatchResult
	scanner := bufio.NewScanner(r)
	// Increase buffer for large responses
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string `json:"type"`
				Message struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
					Model string `json:"model"`
					Usage struct {
						InputTokens  int `json:"input_tokens"`
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			truncated := line
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			log.Warn("batch: skipping malformed JSONL line", "error", err, "line", truncated)
			continue
		}

		br := BatchResult{CustomID: entry.CustomID}
		if entry.Result.Type == "succeeded" {
			var text string
			for _, c := range entry.Result.Message.Content {
				if c.Type == "text" {
					text += c.Text
				}
			}
			br.Response = &Response{
				Content:    stripThinkTags(text),
				Model:      entry.Result.Message.Model,
				TokensUsed: entry.Result.Message.Usage.InputTokens + entry.Result.Message.Usage.OutputTokens,
				Usage: Usage{
					InputTokens:  entry.Result.Message.Usage.InputTokens,
					OutputTokens: entry.Result.Message.Usage.OutputTokens,
				},
			}
		} else {
			br.Error = entry.Result.Error.Message
			if br.Error == "" {
				br.Error = "batch request failed: " + entry.Result.Type
			}
		}
		results = append(results, br)
	}

	return results, scanner.Err()
}

func mapAnthropicBatchStatus(s string) BatchStatus {
	switch s {
	case "ended":
		return BatchEnded
	case "expired":
		return BatchExpired
	case "canceling", "canceled":
		return BatchFailed
	default:
		return BatchInProgress
	}
}

// --- OpenAI BatchProvider ---

func (p *openaiProvider) SubmitBatch(requests []BatchRequest) (string, error) {
	// Step 1: Build JSONL content
	var buf bytes.Buffer
	for _, r := range requests {
		body := p.formatBody(r.Messages, r.Opts, false)
		line := map[string]any{
			"custom_id": r.CustomID,
			"method":    "POST",
			"url":       "/v1/chat/completions",
			"body":      body,
		}
		data, err := json.Marshal(line)
		if err != nil {
			return "", fmt.Errorf("openai batch: marshal request: %w", err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	// Step 2: Upload file
	fileID, err := p.uploadBatchFile(buf.Bytes())
	if err != nil {
		return "", err
	}

	// Step 3: Create batch
	payload := map[string]any{
		"input_file_id":      fileID,
		"endpoint":           "/v1/chat/completions",
		"completion_window":  "24h",
	}

	req, err := http.NewRequest("POST", p.baseURL+"/batches", jsonBody(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai batch: create: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai batch: create returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("openai batch: parse create response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("openai batch: submit returned empty batch ID")
	}

	log.Info("openai batch submitted", "batch_id", result.ID, "requests", len(requests))
	return result.ID, nil
}

func (p *openaiProvider) uploadBatchFile(content []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	_ = w.WriteField("purpose", "batch")
	part, err := w.CreateFormFile("file", "batch_input.jsonl")
	if err != nil {
		return "", err
	}
	part.Write(content)
	w.Close()

	req, err := http.NewRequest("POST", p.baseURL+"/files", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai batch: upload file: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai batch: upload returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result.ID, nil
}

func (p *openaiProvider) PollBatch(batchID string) (*BatchStatusResponse, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/batches/"+batchID, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai batch: poll: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai batch: poll returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		OutputFileID string `json:"output_file_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	status := mapOpenAIBatchStatus(result.Status)
	return &BatchStatusResponse{
		BatchID:    result.ID,
		Status:     status,
		ResultsURL: result.OutputFileID,
	}, nil
}

func (p *openaiProvider) RetrieveBatch(outputFileID string) ([]BatchResult, error) {
	req, err := http.NewRequest("GET", p.baseURL+"/files/"+outputFileID+"/content", nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai batch: retrieve: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai batch: retrieve returned %d: %s", resp.StatusCode, string(body))
	}

	return parseOpenAIBatchResults(resp.Body)
}

func parseOpenAIBatchResults(r io.Reader) ([]BatchResult, error) {
	var results []BatchResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry struct {
			CustomID string `json:"custom_id"`
			Response struct {
				StatusCode int `json:"status_code"`
				Body       struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
					Model string `json:"model"`
					Usage struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
						TotalTokens      int `json:"total_tokens"`
					} `json:"usage"`
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"body"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			truncated := line
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			log.Warn("batch: skipping malformed JSONL line", "error", err, "line", truncated)
			continue
		}

		br := BatchResult{CustomID: entry.CustomID}
		if entry.Response.StatusCode == 200 && len(entry.Response.Body.Choices) > 0 {
			br.Response = &Response{
				Content:    stripThinkTags(entry.Response.Body.Choices[0].Message.Content),
				Model:      entry.Response.Body.Model,
				TokensUsed: entry.Response.Body.Usage.TotalTokens,
				Usage: Usage{
					InputTokens:  entry.Response.Body.Usage.PromptTokens,
					OutputTokens: entry.Response.Body.Usage.CompletionTokens,
				},
			}
		} else {
			br.Error = entry.Response.Body.Error.Message
			if br.Error == "" {
				br.Error = fmt.Sprintf("batch request failed with status %d", entry.Response.StatusCode)
			}
		}
		results = append(results, br)
	}

	return results, scanner.Err()
}

func mapOpenAIBatchStatus(s string) BatchStatus {
	switch s {
	case "completed":
		return BatchEnded
	case "expired":
		return BatchExpired
	case "failed", "cancelled":
		return BatchFailed
	default:
		return BatchInProgress
	}
}
