package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// SourceContent holds extracted text from a source file.
type SourceContent struct {
	Path       string
	Type       string // article, paper, code
	Text       string
	Frontmatter string
	Chunks     []Chunk
	ChunkCount int
}

// Chunk represents a section of a large source.
type Chunk struct {
	Index   int
	Text    string
	Heading string // section heading if available
}

// Extract reads and extracts text from a source file.
func Extract(path string, sourceType string) (*SourceContent, error) {
	ext := strings.ToLower(filepath.Ext(path))

	switch {
	case ext == ".md":
		return extractMarkdown(path, sourceType)
	case ext == ".pdf":
		return extractPDF(path)
	case ext == ".docx":
		return extractDocx(path)
	case ext == ".xlsx":
		return extractXlsx(path)
	case ext == ".pptx":
		return extractPptx(path)
	case ext == ".csv":
		return extractCSV(path)
	case ext == ".epub":
		return extractEpub(path)
	case ext == ".eml" || ext == ".msg":
		return extractEmail(path)
	case isImageFile(ext):
		return extractImage(path)
	case ext == ".txt" || ext == ".log" || ext == ".vtt" || ext == ".srt":
		return extractPlainText(path, sourceType)
	case isCodeFile(ext):
		return extractCode(path)
	default:
		return extractPlainText(path, sourceType) // treat unknown as text
	}
}

// EstimateTokens estimates token count for mixed-script text.
// Latin/ASCII: ~4 chars per token. CJK: ~1.5 tokens per character.
func EstimateTokens(text string) int {
	var cjk, other int
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return int(float64(cjk)*1.5) + other/4
}

// isCJK returns true if the rune is a CJK ideograph or syllable.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hiragana, r)
}

// ChunkIfNeeded splits content into chunks if it exceeds maxTokens.
// Uses adaptive estimation: ~1.5 chars/token for CJK-heavy text, ~4 chars/token for ASCII.
// For table-heavy documents, uses table-aware splitting that preserves table headers.
func ChunkIfNeeded(content *SourceContent, maxTokens int) {
	estimatedTokens := EstimateTokens(content.Text)
	if estimatedTokens <= maxTokens || maxTokens <= 0 {
		content.Chunks = []Chunk{{Index: 0, Text: content.Text}}
		content.ChunkCount = 1
		return
	}

	if isTableHeavy(content.Text) {
		content.Chunks = splitByTableBlocks(content.Text, maxTokens)
	} else if strings.Contains(content.Text, "\n## ") || strings.Contains(content.Text, "\n# ") {
		content.Chunks = splitByHeadings(content.Text, maxTokens)
	} else {
		content.Chunks = splitByParagraphs(content.Text, maxTokens)
	}
	content.ChunkCount = len(content.Chunks)
}

func extractMarkdown(path string, sourceType string) (*SourceContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("extract markdown: %w", err)
	}

	text := string(data)
	var frontmatter string

	// Extract YAML frontmatter
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index(text[4:], "\n---")
		if end >= 0 {
			frontmatter = text[4 : 4+end]
			text = strings.TrimSpace(text[4+end+4:])
		}
	}

	if sourceType == "" || sourceType == "auto" {
		sourceType = "article"
	}

	return &SourceContent{
		Path:        path,
		Type:        sourceType,
		Text:        text,
		Frontmatter: frontmatter,
	}, nil
}

func extractPlainText(path string, sourceType string) (*SourceContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("extract text: %w", err)
	}
	if sourceType == "" || sourceType == "auto" {
		sourceType = "article"
	}
	return &SourceContent{
		Path: path,
		Type: sourceType,
		Text: string(data),
	}, nil
}

func extractCode(path string) (*SourceContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("extract code: %w", err)
	}

	return &SourceContent{
		Path: path,
		Type: "code",
		Text: string(data),
	}, nil
}

func isImageFile(ext string) bool {
	imageExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".webp": true, ".svg": true, ".bmp": true,
	}
	return imageExts[ext]
}

// IsImageSource returns true if the content was extracted from an image file.
// Callers should use vision-capable LLMs for summarization.
func IsImageSource(sc *SourceContent) bool {
	return sc.Type == "image"
}

// extractImage creates a SourceContent that marks the file as requiring vision.
// The actual image bytes are read at summarization time and sent as base64.
func extractImage(path string) (*SourceContent, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("extract image: %w", err)
	}

	return &SourceContent{
		Path: path,
		Type: "image",
		Text: fmt.Sprintf("[Image: %s, %d bytes]", filepath.Base(path), info.Size()),
	}, nil
}

func isCodeFile(ext string) bool {
	codeExts := map[string]bool{
		".go": true, ".py": true, ".js": true, ".ts": true,
		".java": true, ".rs": true, ".c": true, ".cpp": true,
		".rb": true, ".swift": true, ".kt": true,
	}
	return codeExts[ext]
}

// IsTableHeavy returns true if the content is dominated by markdown table rows.
// More than 50% of non-empty lines are table rows (|...|...|).
func IsTableHeavy(sc *SourceContent) bool {
	return isTableHeavy(sc.Text)
}

func isTableHeavy(text string) bool {
	lines := strings.Split(text, "\n")
	total, tableRows := 0, 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		total++
		if strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 3 {
			tableRows++
		}
	}
	if total == 0 {
		return false
	}
	return float64(tableRows)/float64(total) >= 0.5
}

// splitByHeadings splits markdown text on heading boundaries.
func splitByHeadings(text string, maxTokens int) []Chunk {
	lines := strings.Split(text, "\n")
	var chunks []Chunk
	var current strings.Builder
	var currentHeading string
	chunkIdx := 0
	runningTokens := 0

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, Chunk{
				Index:   chunkIdx,
				Text:    strings.TrimSpace(current.String()),
				Heading: currentHeading,
			})
			chunkIdx++
			current.Reset()
			runningTokens = 0
		}
	}

	for _, line := range lines {
		isHeading := strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")

		// Check if adding this line would exceed limit
		lineTokens := EstimateTokens(line)
		if runningTokens+lineTokens > maxTokens && current.Len() > 0 {
			flush()
		}

		if isHeading && current.Len() > 0 {
			flush()
			currentHeading = stripHeadingPrefix(line)
		} else if isHeading {
			currentHeading = stripHeadingPrefix(line)
		}

		current.WriteString(line)
		current.WriteString("\n")
		runningTokens += lineTokens + 1 // +1 for the newline token
	}

	flush()
	return chunks
}


// stripHeadingPrefix removes markdown heading markers (# ## ###) from a line.
func stripHeadingPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	for i < len(line) && line[i] == ' ' {
		i++
	}
	return line[i:]
}

// splitByParagraphs splits on double newlines when no headings exist.
func splitByParagraphs(text string, maxTokens int) []Chunk {
	paragraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	var current strings.Builder
	chunkIdx := 0
	runningTokens := 0
	for _, para := range paragraphs {
		paraTokens := EstimateTokens(para)
		if runningTokens+paraTokens > maxTokens && current.Len() > 0 {
			chunks = append(chunks, Chunk{
				Index: chunkIdx,
				Text:  strings.TrimSpace(current.String()),
			})
			chunkIdx++
			current.Reset()
			runningTokens = 0
		}
		current.WriteString(para)
		current.WriteString("\n\n")
		runningTokens += paraTokens + 2 // +2 for paragraph separator newlines
	}

	if current.Len() > 0 {
		chunks = append(chunks, Chunk{
			Index: chunkIdx,
			Text:  strings.TrimSpace(current.String()),
		})
	}

	return chunks
}

// splitByTableBlocks splits table-heavy documents on row boundaries,
// preserving table headers (first 2 lines: column names + separator) in each chunk.
func splitByTableBlocks(text string, maxTokens int) []Chunk {
	lines := strings.Split(text, "\n")
	var chunks []Chunk
	var current strings.Builder
	chunkIdx := 0

	// Detect the first table header (column names + separator line)
	var tableHeader string
	headerDetected := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !headerDetected && strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 3 {
			// Check if next line is a separator (|---|---|)
			if i+1 < len(lines) {
				nextTrimmed := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(nextTrimmed, "|") && strings.Contains(nextTrimmed, "---") {
					tableHeader = line + "\n" + lines[i+1] + "\n"
					headerDetected = true
				}
			}
			break
		}
	}

	flush := func() {
		t := strings.TrimSpace(current.String())
		if t != "" {
			chunks = append(chunks, Chunk{
				Index: chunkIdx,
				Text:  t,
			})
			chunkIdx++
		}
		current.Reset()
		// Inject table header into next chunk so LLM knows column semantics
		if tableHeader != "" && chunkIdx > 0 {
			current.WriteString(tableHeader)
		}
	}

	for _, line := range lines {
		if EstimateTokens(current.String()+line) > maxTokens && current.Len() > 0 {
			flush()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}

	// Flush remaining
	if current.Len() > 0 {
		t := strings.TrimSpace(current.String())
		if t != "" {
			chunks = append(chunks, Chunk{
				Index: chunkIdx,
				Text:  t,
			})
		}
	}

	return chunks
}

// DetectSourceType guesses source type from file extension.
func DetectSourceType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "paper"
	case ".md", ".txt":
		return "article"
	case ".docx", ".doc":
		return "article"
	case ".xlsx", ".xls", ".csv":
		return "dataset"
	case ".pptx", ".ppt":
		return "article"
	case ".epub":
		return "article"
	case ".eml", ".msg", ".mbox":
		return "article"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp":
		return "image"
	case ".log", ".vtt", ".srt":
		return "article"
	default:
		if isCodeFile(ext) {
			return "code"
		}
		return "article"
	}
}
