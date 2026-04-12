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
	Path          string
	Type          string // article, paper, code
	Text          string
	Frontmatter   string
	Chunks        []Chunk
	ChunkCount    int
	PreExtracted  bool   // whether content was pre-extracted
	Confidence    string // high/medium/low
	ExtractEngine string // extraction engine used
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

// ChunkText is a convenience wrapper that chunks raw text without needing a SourceContent.
// It creates a temporary SourceContent, calls ChunkIfNeeded, and returns the resulting chunks.
func ChunkText(text string, maxTokens int) []Chunk {
	sc := &SourceContent{Text: text}
	ChunkIfNeeded(sc, maxTokens)
	return sc.Chunks
}

// ChunkIfNeeded splits content into chunks if it exceeds maxTokens.
func ChunkIfNeeded(content *SourceContent, maxTokens int) {
	estimatedTokens := EstimateTokens(content.Text)
	if estimatedTokens <= maxTokens || maxTokens <= 0 {
		content.Chunks = []Chunk{{Index: 0, Text: content.Text}}
		content.ChunkCount = 1
		return
	}

	// Split markdown by headings
	if strings.Contains(content.Text, "\n## ") || strings.Contains(content.Text, "\n# ") {
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

// DetectSourceType guesses source type from file extension.
// This is the basic 1-parameter version used as a fallback.
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

// DetectSourceTypeWithSignals guesses source type using file extension,
// content head (first N bytes), and user-configured type signals.
// Signal-based matches (filename keywords, content keywords) take priority
// over extension-based detection.
func DetectSourceTypeWithSignals(path string, contentHead string, typeSignals []TypeSignal) string {
	baseName := filepath.Base(path)
	for _, sig := range typeSignals {
		// Legacy simple pattern match
		if sig.Pattern != "" && strings.Contains(contentHead, sig.Pattern) {
			return sig.Type
		}

		// Filename keyword match
		for _, kw := range sig.FilenameKeywords {
			if strings.Contains(baseName, kw) {
				return sig.Type
			}
		}

		// Content keyword match with threshold
		if len(sig.ContentKeywords) > 0 && contentHead != "" {
			hits := 0
			for _, kw := range sig.ContentKeywords {
				if strings.Contains(contentHead, kw) {
					hits++
				}
			}
			minHits := sig.MinContentHits
			if minHits <= 0 {
				minHits = 1
			}
			if hits >= minHits {
				return sig.Type
			}
		}
	}
	// Fall back to extension-based detection
	return DetectSourceType(path)
}

// TypeSignal mirrors config.TypeSignal so callers in other packages
// can pass signals without importing config in every call site.
type TypeSignal struct {
	Type             string
	Pattern          string   // simple substring match (legacy)
	FilenameKeywords []string // keywords matched against filename
	ContentKeywords  []string // keywords matched against content head
	MinContentHits   int      // minimum content keyword matches required
}

