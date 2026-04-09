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

// ChunkIfNeeded splits content into chunks if it exceeds maxTokens.
// Uses adaptive estimation: ~1.5 chars/token for CJK-heavy text, ~4 chars/token for ASCII.
func ChunkIfNeeded(content *SourceContent, maxTokens int) {
	estimatedTokens := estimateTokenCount(content.Text)
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

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, Chunk{
				Index:   chunkIdx,
				Text:    strings.TrimSpace(current.String()),
				Heading: currentHeading,
			})
			chunkIdx++
			current.Reset()
		}
	}

	for _, line := range lines {
		isHeading := strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")

		// Check if adding this line would exceed limit
		estimatedTokens := estimateTokenCount(current.String() + line)
		if estimatedTokens > maxTokens && current.Len() > 0 {
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
	}

	flush()
	return chunks
}

// estimateTokenCount provides a more accurate token estimate for mixed CJK/ASCII text.
// CJK characters average ~1.5 tokens each; ASCII words average ~1 token per 4 chars.
func estimateTokenCount(text string) int {
	cjkChars := 0
	asciiChars := 0
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjkChars++
		} else {
			asciiChars++
		}
	}
	// CJK: ~1.5 tokens per character; ASCII: ~0.25 tokens per character (4 chars/token)
	return int(float64(cjkChars)*1.5) + asciiChars/4
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
	maxChars := maxTokens * 4

	for _, para := range paragraphs {
		if current.Len()+len(para) > maxChars && current.Len() > 0 {
			chunks = append(chunks, Chunk{
				Index: chunkIdx,
				Text:  strings.TrimSpace(current.String()),
			})
			chunkIdx++
			current.Reset()
		}
		current.WriteString(para)
		current.WriteString("\n\n")
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
