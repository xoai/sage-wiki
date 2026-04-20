package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
)

// geminiKeySanitizer redacts API keys from Gemini URLs in error messages.
var geminiKeySanitizer = regexp.MustCompile(`([?&])key=[^&\s]+`)

// sanitizeGeminiError removes API key query parameters from error text.
func sanitizeGeminiError(s string) string {
	return geminiKeySanitizer.ReplaceAllString(s, "${1}key=REDACTED")
}

// geminiProvider implements the Google Gemini API format.
type geminiProvider struct {
	apiKey          string
	baseURL         string
	uploadBaseURL   string // derived from baseURL; used by batch File API uploads
	downloadBaseURL string // derived from baseURL; used by batch result downloads
	batchHost       string // hostname extracted from baseURL; used for SSRF validation
}

func newGeminiProvider(apiKey string, baseURL string) *geminiProvider {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	// Derive upload/download URL prefixes from baseURL so that a custom proxy
	// or regional endpoint is honoured consistently across all batch operations.
	// Standard pattern: baseURL ends in "/v1beta"; upload is "/upload/v1beta",
	// download is "/download/v1beta". Falls back to baseURL unchanged if the
	// pattern is not found (non-standard endpoint).
	uploadBase := strings.Replace(baseURL, "/v1beta", "/upload/v1beta", 1)
	downloadBase := strings.Replace(baseURL, "/v1beta", "/download/v1beta", 1)
	var batchHost string
	if u, err := url.Parse(baseURL); err == nil {
		batchHost = u.Hostname()
	}
	return &geminiProvider{
		apiKey:          apiKey,
		baseURL:         baseURL,
		uploadBaseURL:   uploadBase,
		downloadBaseURL: downloadBase,
		batchHost:       batchHost,
	}
}

func (p *geminiProvider) Name() string        { return "gemini" }
func (p *geminiProvider) SupportsVision() bool { return true }

func (p *geminiProvider) formatBody(messages []Message, opts CallOpts) (map[string]any, string) {
	var contents []map[string]any
	var systemInstruction string

	for _, m := range messages {
		if m.Role == "system" {
			systemInstruction = m.Content
			continue
		}

		role := m.Role
		if role == "assistant" {
			role = "model"
		}

		if m.ImageBase64 != "" {
			contents = append(contents, map[string]any{
				"role": role,
				"parts": []any{
					map[string]any{
						"inlineData": map[string]string{
							"mimeType": m.ImageMime,
							"data":     m.ImageBase64,
						},
					},
					map[string]string{"text": m.Content},
				},
			})
		} else {
			contents = append(contents, map[string]any{
				"role": role,
				"parts": []map[string]string{
					{"text": m.Content},
				},
			})
		}
	}

	body := map[string]any{
		"contents": contents,
	}

	if systemInstruction != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]string{
				{"text": systemInstruction},
			},
		}
	}

	config := map[string]any{}
	if opts.MaxTokens > 0 {
		config["maxOutputTokens"] = opts.MaxTokens
	}
	if opts.Temperature > 0 {
		config["temperature"] = opts.Temperature
	}
	if len(config) > 0 {
		body["generationConfig"] = config
	}

	model := opts.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}

	return body, model
}

func (p *geminiProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	body, model := p.formatBody(messages, opts)

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, model, p.apiKey)

	req, err := http.NewRequest("POST", url, jsonBody(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (p *geminiProvider) FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	body, model := p.formatBody(messages, opts)

	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?key=%s&alt=sse", p.baseURL, model, p.apiKey)

	req, err := http.NewRequest("POST", url, jsonBody(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// --- CachingProvider implementation ---

func (p *geminiProvider) SetupCache(systemPrompt string, model string) (string, error) {
	if model == "" {
		model = "gemini-2.5-flash"
	}

	body := map[string]any{
		"model": "models/" + model,
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]string{{"text": systemPrompt}},
			},
		},
	}

	url := fmt.Sprintf("%s/cachedContents?key=%s", p.baseURL, p.apiKey)
	req, err := http.NewRequest("POST", url, jsonBody(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini: cache setup request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini: cache setup failed (%d): %s", resp.StatusCode, sanitizeGeminiError(string(respBody)))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("gemini: parse cache response: %w", err)
	}

	return result.Name, nil
}

func (p *geminiProvider) FormatCachedRequest(cacheID string, messages []Message, opts CallOpts) (*http.Request, error) {
	if cacheID == "" {
		// No cache — use regular request
		return p.FormatRequest(messages, opts)
	}

	body, model := p.formatBody(messages, opts)
	body["cachedContent"] = cacheID
	// Remove systemInstruction — it's already in the cached content
	delete(body, "systemInstruction")

	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, model, p.apiKey)

	req, err := http.NewRequest("POST", url, jsonBody(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (p *geminiProvider) TeardownCache(cacheID string) error {
	if cacheID == "" {
		return nil
	}

	url := fmt.Sprintf("%s/%s?key=%s", p.baseURL, cacheID, p.apiKey)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gemini: cache teardown failed: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("gemini: cache delete returned %d", resp.StatusCode)
	}
	return nil
}

func (p *geminiProvider) ParseStreamChunk(data []byte) (string, bool) {
	var chunk struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return "", false
	}
	if len(chunk.Candidates) == 0 {
		return "", false
	}
	var text string
	for _, part := range chunk.Candidates[0].Content.Parts {
		text += part.Text
	}
	done := chunk.Candidates[0].FinishReason == "STOP" || chunk.Candidates[0].FinishReason == "MAX_TOKENS"
	return text, done
}

func (p *geminiProvider) ParseResponse(body []byte) (*Response, error) {
	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
		ModelVersion string `json:"modelVersion"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("gemini: parse: %w", err)
	}

	// Check for API error
	if result.Error.Message != "" {
		return nil, fmt.Errorf("gemini: API error %d (%s): %s", result.Error.Code, result.Error.Status, result.Error.Message)
	}

	// Check for safety block
	if result.PromptFeedback.BlockReason != "" {
		return nil, fmt.Errorf("gemini: blocked by safety filter: %s", result.PromptFeedback.BlockReason)
	}

	if len(result.Candidates) == 0 {
		raw := string(body)
		if len(raw) > 300 {
			raw = raw[:300] + "..."
		}
		return nil, fmt.Errorf("gemini: no candidates in response. Raw: %s", raw)
	}

	// Handle MAX_TOKENS with empty content (thinking models use tokens internally)
	if len(result.Candidates[0].Content.Parts) == 0 {
		if result.Candidates[0].FinishReason == "MAX_TOKENS" {
			return &Response{
				Content:    "",
				Model:      result.ModelVersion,
				TokensUsed: result.UsageMetadata.TotalTokenCount,
			}, nil
		}
		raw := string(body)
		if len(raw) > 300 {
			raw = raw[:300] + "..."
		}
		return nil, fmt.Errorf("gemini: empty content (finish: %s). Raw: %s", result.Candidates[0].FinishReason, raw)
	}

	var text string
	for _, part := range result.Candidates[0].Content.Parts {
		text += part.Text
	}

	return &Response{
		Content:    text,
		Model:      result.ModelVersion,
		TokensUsed: result.UsageMetadata.TotalTokenCount,
		Usage: Usage{
			InputTokens:  result.UsageMetadata.PromptTokenCount,
			OutputTokens: result.UsageMetadata.CandidatesTokenCount,
			CachedTokens: result.UsageMetadata.CachedContentTokenCount,
		},
	}, nil
}

// --- BatchProvider implementation ---
//
// Gemini batch jobs use the Developer API (api-key auth, no OAuth needed).
// Flow: upload JSONL input → submit batch → poll until done → download JSONL results.
// Batch quota is separate from live quota and runs at 50% of standard pricing.

// geminiBatchState maps Gemini job state strings to BatchStatus.
func geminiBatchState(state string) BatchStatus {
	switch state {
	case "JOB_STATE_SUCCEEDED":
		return BatchEnded
	case "JOB_STATE_EXPIRED":
		return BatchExpired
	case "JOB_STATE_FAILED", "JOB_STATE_CANCELLED":
		return BatchFailed
	default:
		return BatchInProgress
	}
}

// uploadBatchInputFile uploads a JSONL payload to the Gemini File API.
// Returns the resource name (e.g. "files/abc123") used to reference the file in batch submission.
func (p *geminiProvider) uploadBatchInputFile(jsonl []byte) (string, error) {
	const boundary = "sage_wiki_batch_boundary"

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		return "", fmt.Errorf("gemini batch: set boundary: %w", err)
	}

	// Metadata part: tells the File API the MIME type of the content.
	metaPart, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"application/json; charset=utf-8"},
	})
	if err != nil {
		return "", fmt.Errorf("gemini batch: create metadata part: %w", err)
	}
	metaJSON, _ := json.Marshal(map[string]any{
		"file": map[string]string{
			"displayName": "sage-wiki-batch-input",
			"mimeType":    "text/plain",
		},
	})
	if _, err := metaPart.Write(metaJSON); err != nil {
		return "", fmt.Errorf("gemini batch: write metadata: %w", err)
	}

	// Data part: the actual JSONL content.
	dataPart, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type": {"text/plain"},
	})
	if err != nil {
		return "", fmt.Errorf("gemini batch: create data part: %w", err)
	}
	if _, err := dataPart.Write(jsonl); err != nil {
		return "", fmt.Errorf("gemini batch: write data: %w", err)
	}
	w.Close()

	uploadURL := fmt.Sprintf("%s/files?key=%s", p.uploadBaseURL, p.apiKey)
	req, err := http.NewRequest("POST", uploadURL, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)
	req.Header.Set("X-Goog-Upload-Protocol", "multipart")

	client := http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini batch: upload file: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini batch: upload returned %d: %s", resp.StatusCode, sanitizeGeminiError(string(body)))
	}

	var result struct {
		File struct {
			Name string `json:"name"`
		} `json:"file"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("gemini batch: parse upload response: %w", err)
	}
	if result.File.Name == "" {
		return "", fmt.Errorf("gemini batch: upload returned empty file name")
	}

	return result.File.Name, nil
}

// SubmitBatch implements BatchProvider for Gemini.
// Serialises requests as JSONL (one per line, with a "key" field for correlation),
// uploads to the File API, then submits an async batch job.
func (p *geminiProvider) SubmitBatch(requests []BatchRequest) (string, error) {
	if len(requests) == 0 {
		return "", fmt.Errorf("gemini batch: no requests")
	}

	// All requests in a batch share the same model; use the first request's model.
	model := requests[0].Opts.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}

	// Build JSONL — one GenerateContentRequest per line, keyed by CustomID.
	var buf bytes.Buffer
	for _, r := range requests {
		body, _ := p.formatBody(r.Messages, r.Opts)
		line := map[string]any{
			"key":     r.CustomID,
			"request": body,
		}
		data, err := json.Marshal(line)
		if err != nil {
			return "", fmt.Errorf("gemini batch: marshal request %q: %w", r.CustomID, err)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}

	// Upload JSONL to the File API.
	fileName, err := p.uploadBatchInputFile(buf.Bytes())
	if err != nil {
		return "", err
	}
	log.Info("gemini batch: uploaded input file", "file", fileName, "requests", len(requests))

	// Submit the batch job referencing the uploaded file.
	payload := map[string]any{
		"batch": map[string]any{
			"displayName": "sage-wiki-batch",
			"inputConfig": map[string]any{
				"fileName": fileName,
			},
		},
	}

	submitURL := fmt.Sprintf("%s/models/%s:batchGenerateContent?key=%s", p.baseURL, model, p.apiKey)
	req, err := http.NewRequest("POST", submitURL, jsonBody(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	hc := http.Client{Timeout: 120 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini batch: submit: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini batch: submit returned %d: %s", resp.StatusCode, sanitizeGeminiError(string(respBody)))
	}

	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("gemini batch: parse submit response: %w", err)
	}
	if result.Name == "" {
		return "", fmt.Errorf("gemini batch: submit returned empty batch name")
	}

	log.Info("gemini batch submitted", "batch", result.Name, "requests", len(requests))
	return result.Name, nil
}

// PollBatch checks the status of a Gemini batch job.
// batchID is the resource name returned by SubmitBatch (e.g. "batches/abc123").
func (p *geminiProvider) PollBatch(batchID string) (*BatchStatusResponse, error) {
	pollURL := fmt.Sprintf("%s/%s?key=%s", p.baseURL, batchID, p.apiKey)
	req, err := http.NewRequest("GET", pollURL, nil)
	if err != nil {
		return nil, err
	}

	hc := http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini batch: poll: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini batch: poll returned %d: %s", resp.StatusCode, sanitizeGeminiError(string(body)))
	}

	var result struct {
		Name     string `json:"name"`
		State    string `json:"state"`
		Response struct {
			ResponsesFile string `json:"responsesFile"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("gemini batch: parse poll response: %w", err)
	}

	return &BatchStatusResponse{
		BatchID:    result.Name,
		Status:     geminiBatchState(result.State),
		ResultsURL: result.Response.ResponsesFile, // e.g. "files/abc123-responses"
	}, nil
}

// RetrieveBatch downloads and parses results from a completed Gemini batch.
// resultsRef is the file resource name from PollBatch (e.g. "files/abc123-responses").
func (p *geminiProvider) RetrieveBatch(resultsRef string) ([]BatchResult, error) {
	// Validate the download URL points to the expected host to prevent SSRF.
	// p.batchHost is derived from p.baseURL so custom endpoints are honoured.
	if err := validateBatchURL(
		fmt.Sprintf("%s/%s:download", p.downloadBaseURL, resultsRef),
		p.batchHost,
	); err != nil {
		return nil, fmt.Errorf("gemini batch: %w", err)
	}

	downloadURL := fmt.Sprintf("%s/%s:download?alt=media&key=%s", p.downloadBaseURL, resultsRef, p.apiKey)
	req, err := http.NewRequest("GET", downloadURL, nil)
	if err != nil {
		return nil, err
	}

	hc := http.Client{Timeout: 300 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini batch: download results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gemini batch: download returned %d: %s", resp.StatusCode, sanitizeGeminiError(string(body)))
	}

	return parseGeminiBatchResults(resp.Body)
}

// parseGeminiBatchResults parses the JSONL results file returned by a completed Gemini batch job.
// Each line is: {"key": "<CustomID>", "response": <GenerateContentResponse>}
// or:            {"key": "<CustomID>", "status": {"code": N, "message": "..."}}  on per-request error.
func parseGeminiBatchResults(r io.Reader) ([]BatchResult, error) {
	var results []BatchResult
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry struct {
			Key      string `json:"key"`
			Response *struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
					FinishReason string `json:"finishReason"`
				} `json:"candidates"`
				UsageMetadata struct {
					PromptTokenCount     int `json:"promptTokenCount"`
					CandidatesTokenCount int `json:"candidatesTokenCount"`
					TotalTokenCount      int `json:"totalTokenCount"`
				} `json:"usageMetadata"`
				ModelVersion string `json:"modelVersion"`
			} `json:"response"`
			Status *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"status"`
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			truncated := line
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			log.Warn("gemini batch: skipping malformed JSONL line", "error", err, "line", truncated)
			continue
		}

		br := BatchResult{CustomID: entry.Key}

		switch {
		case entry.Status != nil && entry.Status.Code != 0:
			br.Error = fmt.Sprintf("code %d: %s", entry.Status.Code, entry.Status.Message)
		case entry.Response != nil && len(entry.Response.Candidates) > 0:
			var text string
			for _, part := range entry.Response.Candidates[0].Content.Parts {
				text += part.Text
			}
			br.Response = &Response{
				Content:    stripThinkTags(text),
				Model:      entry.Response.ModelVersion,
				TokensUsed: entry.Response.UsageMetadata.TotalTokenCount,
				Usage: Usage{
					InputTokens:  entry.Response.UsageMetadata.PromptTokenCount,
					OutputTokens: entry.Response.UsageMetadata.CandidatesTokenCount,
				},
			}
		default:
			br.Error = "empty response"
		}

		results = append(results, br)
	}

	return results, scanner.Err()
}
