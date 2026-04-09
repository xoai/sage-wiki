package extract

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extractPDF extracts text from a PDF file.
// Tries pdftotext (poppler) first for better font/encoding support,
// falls back to the pure Go library if pdftotext is not available.
func extractPDF(path string) (*SourceContent, error) {
	// Try pdftotext first
	if text := extractPDFPoppler(path); text != "" {
		return &SourceContent{
			Path: path,
			Type: "paper",
			Text: text,
		}, nil
	}

	// Fallback to Go library
	return extractPDFGo(path)
}

// extractPDFPoppler uses pdftotext (poppler) for extraction.
func extractPDFPoppler(path string) string {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		return ""
	}

	out, err := exec.Command(pdftotext, path, "-").Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// extractPDFGo uses the pure Go PDF library.
func extractPDFGo(path string) (*SourceContent, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("extract pdf: open: %w", err)
	}
	defer f.Close()

	var text strings.Builder
	numPages := r.NumPage()

	for i := 1; i <= numPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}

		content, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		text.WriteString(content)
		text.WriteString("\n\n")
	}

	extracted := strings.TrimSpace(text.String())
	if extracted == "" {
		return nil, fmt.Errorf("extract pdf: no text content in %s (scanned PDF or images only)", path)
	}

	return &SourceContent{
		Path: path,
		Type: "paper",
		Text: extracted,
	}, nil
}
