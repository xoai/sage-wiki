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
	language string,
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

			result := summarizeOne(projectDir, outputDir, info, client, model, maxTokens, userTZ, language)
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
	language string,
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
	// Override to structured template if document matches structured heuristics
	if isStructuredDocument(content.Text) {
		templateName = "summarize_structured"
		log.Info("detected structured document, using structured template", "source", info.Path)
	}

	// Build language instruction suffix
	langSuffix := ""
	if language != "" {
		langSuffix = fmt.Sprintf("\n\nIMPORTANT: Write the summary in %s.", languageDisplayName(language))
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
			{Role: "user", Content: prompt + langSuffix + "\n\n---\n\nSource content:\n\n" + content.Text},
		}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
		if err != nil {
			result.Error = fmt.Errorf("llm call: %w", err)
			return result
		}

		summaryText = resp.Content
	} else {
		// Multi-chunk: check for table-heavy documents first
		tableHeavy := extract.IsTableHeavy(content)

		if tableHeavy && content.ChunkCount >= 4 {
			// Large table document: use specialized schema→sample→synthesis strategy
			text, err := summarizeTableHeavyDocument(info, content, client, model, maxTokens, langSuffix)
			if err != nil {
				result.Error = err
				return result
			}
			summaryText = text
		} else {
			// Standard multi-chunk: summarize each chunk, then synthesize hierarchically
			chunkSummaries, err := summarizeChunks(content.Chunks, info, templateName, content.Type, client, model, maxTokens, langSuffix)
			if err != nil {
				result.Error = err
				return result
			}

			// Hierarchical synthesis: reduce in groups until we have a single summary
			summaryText, err = synthesizeHierarchical(chunkSummaries, info.Path, client, model, maxTokens)
			if err != nil {
				result.Error = err
				return result
			}
		}
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

const (
	// minChunkTokenBudget is the minimum output tokens per chunk summary.
	// Below this, LLMs produce empty or unusable output.
	minChunkTokenBudget = 200

	// synthesisGroupSize is the max number of summaries per synthesis call.
	// Keeps each synthesis step at a manageable compression ratio (~8x).
	synthesisGroupSize = 8
)

// summarizeChunks summarizes each chunk with a minimum token budget.
// When the budget per chunk would fall below minChunkTokenBudget, chunks
// are grouped together to maintain quality.
func summarizeChunks(
	chunks []extract.Chunk,
	info SourceInfo,
	templateName string,
	sourceType string,
	client *llm.Client,
	model string,
	maxTokens int,
	langSuffix string,
) ([]string, error) {
	// Group chunks if per-chunk budget is too low
	groups := groupChunks(chunks, maxTokens)
	if len(groups) == 0 {
		return nil, fmt.Errorf("summarize: no chunk groups for %q", info.Path)
	}

	var summaries []string
	for gi, group := range groups {
		perGroupBudget := maxTokens / len(groups)
		if perGroupBudget < minChunkTokenBudget {
			perGroupBudget = minChunkTokenBudget
		}

		// Combine text from all chunks in the group
		var groupText strings.Builder
		for i, chunk := range group {
			if i > 0 {
				groupText.WriteString("\n\n---\n\n")
			}
			if chunk.Heading != "" {
				groupText.WriteString("## ")
				groupText.WriteString(chunk.Heading)
				groupText.WriteString("\n\n")
			}
			groupText.WriteString(chunk.Text)
		}

		prompt, err := prompts.Render(templateName, prompts.SummarizeData{
			SourcePath: info.Path,
			SourceType: sourceType,
			MaxTokens:  perGroupBudget,
		})
		if err != nil {
			return nil, fmt.Errorf("group %d render prompt: %w", gi, err)
		}

		resp, err := client.ChatCompletion([]llm.Message{
			{Role: "system", Content: "You are summarizing a section of a larger document."},
			{Role: "user", Content: prompt + langSuffix + "\n\n---\n\nSection:\n\n" + groupText.String()},
		}, llm.CallOpts{Model: model, MaxTokens: perGroupBudget})
		if err != nil {
			return nil, fmt.Errorf("group %d llm: %w", gi, err)
		}

		// Empty response guard
		if strings.TrimSpace(resp.Content) == "" {
			return nil, fmt.Errorf("group %d: LLM returned empty summary for %q (chunks %d-%d)",
				gi, info.Path, group[0].Index, group[len(group)-1].Index)
		}

		summaries = append(summaries, resp.Content)
	}

	return summaries, nil
}

// groupChunks groups chunks to ensure each group gets at least minChunkTokenBudget.
func groupChunks(chunks []extract.Chunk, maxTokens int) [][]extract.Chunk {
	if len(chunks) == 0 {
		return nil
	}
	perChunkBudget := maxTokens / len(chunks)
	if perChunkBudget >= minChunkTokenBudget {
		// Each chunk gets enough budget — no grouping needed
		groups := make([][]extract.Chunk, len(chunks))
		for i, c := range chunks {
			groups[i] = []extract.Chunk{c}
		}
		return groups
	}

	// Calculate how many groups we need so each gets >= minChunkTokenBudget
	maxGroups := maxTokens / minChunkTokenBudget
	if maxGroups < 1 {
		maxGroups = 1
	}

	chunksPerGroup := (len(chunks) + maxGroups - 1) / maxGroups // ceiling division
	var groups [][]extract.Chunk
	for i := 0; i < len(chunks); i += chunksPerGroup {
		end := i + chunksPerGroup
		if end > len(chunks) {
			end = len(chunks)
		}
		groups = append(groups, chunks[i:end])
	}

	return groups
}

// synthesizeHierarchical reduces summaries in tiers of synthesisGroupSize
// until a single final summary remains.
func synthesizeHierarchical(summaries []string, sourcePath string, client *llm.Client, model string, maxTokens int) (string, error) {
	if len(summaries) == 0 {
		return "", fmt.Errorf("synthesize: no summaries to combine for %q", sourcePath)
	}
	for len(summaries) > 1 {
		var nextLevel []string

		for i := 0; i < len(summaries); i += synthesisGroupSize {
			end := i + synthesisGroupSize
			if end > len(summaries) {
				end = len(summaries)
			}
			group := summaries[i:end]

			if len(group) == 1 {
				nextLevel = append(nextLevel, group[0])
				continue
			}

			synthesisPrompt := fmt.Sprintf(
				"Combine these %d section summaries into a single coherent summary of the source document %q.\n\n%s",
				len(group), sourcePath, strings.Join(group, "\n\n---\n\n"),
			)

			resp, err := client.ChatCompletion([]llm.Message{
				{Role: "system", Content: "You are synthesizing partial summaries into a final summary."},
				{Role: "user", Content: synthesisPrompt},
			}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
			if err != nil {
				return "", fmt.Errorf("synthesis llm: %w", err)
			}

			if strings.TrimSpace(resp.Content) == "" {
				return "", fmt.Errorf("synthesis returned empty result for %q", sourcePath)
			}

			nextLevel = append(nextLevel, resp.Content)
		}

		summaries = nextLevel
	}

	return summaries[0], nil
}

// summarizeTableHeavyDocument uses a schema→sample→synthesis strategy for large table documents.
func summarizeTableHeavyDocument(
	info SourceInfo,
	content *extract.SourceContent,
	client *llm.Client,
	model string,
	maxTokens int,
	langSuffix string,
) (string, error) {
	// Pass 1: Extract schema from first chunk (has table header)
	schemaPrompt := fmt.Sprintf(
		`This is chunk 1 of %d from a large markdown table document: %q.
Extract:
1. Table column names and their semantic meaning
2. Estimated total row count (based on this chunk's row density and total chunks)
3. Sample 3-5 representative rows in markdown table format
4. Key patterns you observe (value ranges, naming conventions, distributions)

Output as markdown.%s`,
		len(content.Chunks), info.Path, langSuffix,
	)

	schemaTokens := maxTokens / 2
	schemaResp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are extracting schema and statistics from a large data table."},
		{Role: "user", Content: schemaPrompt + "\n\n---\n\n" + content.Chunks[0].Text},
	}, llm.CallOpts{Model: model, MaxTokens: schemaTokens})
	if err != nil {
		return "", fmt.Errorf("table schema extraction: %w", err)
	}

	// Pass 2: Sample middle chunks for value diversity
	var samples []string
	sampleChunks := selectSampleChunks(content.Chunks, 3)
	for _, chunk := range sampleChunks {
		samplePrompt := fmt.Sprintf(
			`From this chunk of the table %q, extract 3-5 representative or interesting rows as a markdown table fragment. Focus on diversity — pick rows that show the range of values.%s`,
			info.Path, langSuffix,
		)
		sr, err := client.ChatCompletion([]llm.Message{
			{Role: "system", Content: "You are sampling representative rows from a large data table."},
			{Role: "user", Content: samplePrompt + "\n\n---\n\n" + chunk.Text},
		}, llm.CallOpts{Model: model, MaxTokens: 300})
		if err != nil {
			log.Warn("table sample failed", "source", info.Path, "chunk", chunk.Index, "error", err)
			continue // skip failed samples, non-fatal
		}
		samples = append(samples, sr.Content)
	}
	if len(samples) == 0 && len(sampleChunks) > 0 {
		log.Warn("all table samples failed, synthesis will rely on schema only", "source", info.Path)
	}

	// Pass 3: Synthesize into structured table summary
	synthesisPrompt := fmt.Sprintf(
		`Create a structured summary of the large table document %q.

You have schema analysis and %d sample chunks below.

Write a summary with these sections:
## Table Overview
What this table is, estimated row count, and its purpose.

## Column Definitions
Each column: name, type/meaning, example values.

## Data Patterns
Naming conventions, value distributions, notable patterns.

## Representative Records
Markdown table with 5-10 diverse rows showing the range of data.%s

---
SCHEMA ANALYSIS:
%s

---
SAMPLES:
%s`,
		info.Path,
		len(samples),
		langSuffix,
		schemaResp.Content,
		strings.Join(samples, "\n\n---\n\n"),
	)

	finalResp, err := client.ChatCompletion([]llm.Message{
		{Role: "system", Content: "You are creating a structured summary of a large database table for a knowledge wiki."},
		{Role: "user", Content: synthesisPrompt},
	}, llm.CallOpts{Model: model, MaxTokens: maxTokens})
	if err != nil {
		return "", fmt.Errorf("table synthesis: %w", err)
	}

	return finalResp.Content, nil
}

// selectSampleChunks picks up to n evenly-spaced chunks from the middle of the list,
// excluding the first chunk (which is used for schema extraction).
func selectSampleChunks(chunks []extract.Chunk, n int) []extract.Chunk {
	if len(chunks) <= 1 {
		return nil
	}
	rest := chunks[1:] // skip first (schema) chunk
	if len(rest) <= n {
		return rest
	}
	var selected []extract.Chunk
	step := len(rest) / n
	for i := 0; i < n; i++ {
		selected = append(selected, rest[i*step])
	}
	return selected
}

// isStructuredDocument uses heuristics to detect whether a document is a
// structured reference (registry, dictionary, catalog, spec) rather than
// a narrative article. Structured documents benefit from entry-preserving
// summarization instead of narrative compression.
func isStructuredDocument(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 10 {
		return false
	}

	// Heuristic 1: High density of repeated heading patterns (### Name + fields)
	h3Count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "### ") || strings.HasPrefix(line, "#### ") {
			h3Count++
		}
	}
	if h3Count >= 5 {
		return true
	}

	// Heuristic 2: High density of definition-list patterns (- **Key**: value)
	boldKeyCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- **") || strings.HasPrefix(trimmed, "* **") {
			boldKeyCount++
		}
	}
	if boldKeyCount >= 10 {
		return true
	}

	// Heuristic 3: High density of code blocks (SQL, config, etc.)
	codeBlockCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			codeBlockCount++
		}
	}
	if codeBlockCount >= 6 && h3Count >= 3 {
		return true
	}

	// Heuristic 4: Table-heavy documents (| col | col |)
	tableRowCount := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "|") && strings.Count(line, "|") >= 3 {
			tableRowCount++
		}
	}
	if tableRowCount >= 10 {
		return true
	}

	return false
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
