package compiler

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/extract"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// SummaryResult holds the output of summarizing a source.
type SummaryResult struct {
	SourcePath  string
	SummaryPath string
	Summary     string
	Concepts    []string
	ChunkCount  int
	Error       error
}

// Summarize processes sources through Pass 1, producing summaries.
func Summarize(
	projectDir string,
	outputDir string,
	sources []SourceInfo,
	client *llm.Client,
	model string,
	maxTokens int,
	maxParallel int,
	userTZ *time.Location,
) []SummaryResult {
	if maxParallel <= 0 {
		maxParallel = 4
	}

	results := make([]SummaryResult, len(sources))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	var done atomic.Int32
	total := len(sources)

	for i, src := range sources {
		wg.Add(1)
		sem <- struct{}{}

		go func(idx int, info SourceInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			result := summarizeOne(projectDir, outputDir, info, client, model, maxTokens, userTZ)
			results[idx] = result

			n := int(done.Add(1))
			if result.Error != nil {
				log.Error("summarize failed", "progress", fmt.Sprintf("%d/%d", n, total), "source", info.Path, "error", result.Error)
			} else {
				log.Info("summarized", "progress", fmt.Sprintf("%d/%d", n, total), "source", info.Path)
			}
		}(i, src)
	}

	wg.Wait()
	return results
}

func summarizeOne(
	projectDir string,
	outputDir string,
	info SourceInfo,
	client *llm.Client,
	model string,
	maxTokens int,
	userTZ *time.Location,
) SummaryResult {
	result := SummaryResult{SourcePath: info.Path}

	// Extract source content
	absPath := filepath.Join(projectDir, info.Path)
	content, err := extract.Extract(absPath, info.Type)
	if err != nil {
		result.Error = fmt.Errorf("extract: %w", err)
		return result
	}

	var summaryText string

	// Handle image sources — use vision if available
	if extract.IsImageSource(content) {
		text, err := summarizeImage(projectDir, info, client, model, maxTokens)
		if err != nil {
			result.Error = err
			return result
		}
		summaryText = text
		return writeSummaryFile(projectDir, outputDir, info, content, summaryText, result, userTZ)
	}

	// Chunk if needed
	extract.ChunkIfNeeded(content, maxTokens*2) // Allow 2x for input
	result.ChunkCount = content.ChunkCount

	// Select prompt template — try type-specific first, fall back to article
	templateName := "summarize_" + content.Type
	if _, err := prompts.Render(templateName, prompts.SummarizeData{}); err != nil {
		templateName = "summarize_article" // fallback for unknown types
	}

	if content.ChunkCount <= 1 {
		// Single-chunk summarization
		prompt, err := prompts.Render(templateName, prompts.SummarizeData{
			SourcePath: info.Path,
			SourceType: content.Type,
			MaxTokens:  maxTokens,
		})
		if err != nil {
			result.Error = fmt.Errorf("render prompt: %w", err)
			return result
		}

		resp, err := client.ChatCompletion([]llm.Message{
			{Role: "system", Content: "You are a research assistant creating structured summaries for a personal knowledge wiki."},
			{Role: "user", Content: prompt + "\n\n---\n\nSource content:\n\n" + content.Text},
		}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
		if err != nil {
			result.Error = fmt.Errorf("llm call: %w", err)
			return result
		}

		summaryText = resp.Content
	} else {
		// Multi-chunk: summarize each chunk, then synthesize
		var chunkSummaries []string
		for _, chunk := range content.Chunks {
			prompt, err := prompts.Render(templateName, prompts.SummarizeData{
				SourcePath: info.Path,
				SourceType: content.Type,
				MaxTokens:  maxTokens / content.ChunkCount,
			})
			if err != nil {
				result.Error = fmt.Errorf("chunk %d render prompt: %w", chunk.Index, err)
				return result
			}

			resp, err := client.ChatCompletion([]llm.Message{
				{Role: "system", Content: "You are summarizing a section of a larger document."},
				{Role: "user", Content: prompt + "\n\n---\n\nSection:\n\n" + chunk.Text},
			}, llm.CallOpts{Model: model, MaxTokens: maxTokens / content.ChunkCount})
			if err != nil {
				result.Error = fmt.Errorf("chunk %d llm: %w", chunk.Index, err)
				return result
			}
			chunkSummaries = append(chunkSummaries, resp.Content)
		}

		// Synthesize chunk summaries
		synthesisPrompt := fmt.Sprintf(
			"Combine these %d section summaries into a single coherent summary of the source document %q.\n\n%s",
			len(chunkSummaries), info.Path, strings.Join(chunkSummaries, "\n\n---\n\n"),
		)

		resp, err := client.ChatCompletion([]llm.Message{
			{Role: "system", Content: "You are synthesizing partial summaries into a final summary."},
			{Role: "user", Content: synthesisPrompt},
		}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
		if err != nil {
			result.Error = fmt.Errorf("synthesis llm: %w", err)
			return result
		}
		summaryText = resp.Content
	}

	return writeSummaryFile(projectDir, outputDir, info, content, summaryText, result, userTZ)
}

func writeSummaryFile(projectDir, outputDir string, info SourceInfo, content *extract.SourceContent, summaryText string, result SummaryResult, loc *time.Location) SummaryResult {
	summaryDir := filepath.Join(projectDir, outputDir, "summaries")
	os.MkdirAll(summaryDir, 0755)

	baseName := strings.TrimSuffix(filepath.Base(info.Path), filepath.Ext(info.Path))
	summaryPath := filepath.Join(outputDir, "summaries", baseName+".md")
	absOutputPath := filepath.Join(projectDir, summaryPath)

	frontmatter := fmt.Sprintf(`---
source: %s
source_type: %s
compiled_at: %s
chunk_count: %d
---

`, info.Path, content.Type, timeNow(loc), content.ChunkCount)

	if err := os.WriteFile(absOutputPath, []byte(frontmatter+summaryText), 0644); err != nil {
		result.Error = fmt.Errorf("write summary: %w", err)
		return result
	}

	result.SummaryPath = summaryPath
	result.Summary = summaryText
	return result
}

func summarizeImage(projectDir string, info SourceInfo, client *llm.Client, model string, maxTokens int) (string, error) {
	if !client.SupportsVision() {
		return "", fmt.Errorf("skipping image %s — LLM provider does not support vision", info.Path)
	}

	absPath := filepath.Join(projectDir, info.Path)
	imgData, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}

	mimeType := detectImageMime(info.Path)
	b64 := base64.StdEncoding.EncodeToString(imgData)

	prompt := fmt.Sprintf("Describe this image from a knowledge base.\nSource: %s\n\nProvide:\n1. A brief caption\n2. What the image depicts (diagram, chart, photo, screenshot, etc.)\n3. Key information conveyed\n4. Any text visible in the image\n5. Concepts this relates to", info.Path)

	resp, err := client.ChatCompletionWithImage([]llm.Message{
		{Role: "system", Content: "You are a research assistant describing images for a personal knowledge wiki."},
	}, prompt, b64, mimeType, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		return "", fmt.Errorf("vision LLM: %w", err)
	}

	return resp.Content, nil
}

func detectImageMime(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}
