package extract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

// extractPDF extracts text from a PDF file.
// Tries pdftotext (poppler) first for better font/encoding support,
// falls back to the pure Go library, and finally attempts OCR via
// ocrmypdf if the PDF appears to be a scanned image-only document.
func extractPDF(path string) (*SourceContent, error) {
	// Try pdftotext first
	if text := extractPDFPoppler(path); text != "" {
		return &SourceContent{
			Path:          path,
			Type:          "paper",
			Text:          text,
			ExtractEngine: "pdftotext",
		}, nil
	}

	// Fallback to Go library
	sc, err := extractPDFGo(path)
	if err == nil {
		return sc, nil
	}

	// Last resort: OCR via ocrmypdf (handles scanned/image-only PDFs)
	if text := extractPDFOCR(path); text != "" {
		return &SourceContent{
			Path:          path,
			Type:          "paper",
			Text:          text,
			ExtractEngine: "ocrmypdf",
		}, nil
	}

	return nil, err // return the original "no text content" error
}

// extractPDFPoppler uses pdftotext (poppler) for extraction.
func extractPDFPoppler(path string) string {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pdftotext, path, "-").Output()
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
		Path:          path,
		Type:          "paper",
		Text:          extracted,
		ExtractEngine: "pdf-go",
	}, nil
}

// extractPDFOCR runs ocrmypdf to add a text layer to a scanned PDF, then
// extracts that text. It writes a temporary PDF and cleans up afterwards.
// Returns empty string if ocrmypdf is not installed or OCR fails.
func extractPDFOCR(path string) string {
	ocrmypdf, err := exec.LookPath("ocrmypdf")
	if err != nil {
		return "" // ocrmypdf not installed
	}

	// Create a temp file for the OCR output
	tmp, err := os.CreateTemp("", "sagwiki-ocr-*.pdf")
	if err != nil {
		return ""
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	// --skip-text: don't re-OCR pages that already have text
	// --quiet:     suppress progress output
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ocrmypdf, "--skip-text", "--quiet", path, tmp.Name())
	if err := cmd.Run(); err != nil {
		return ""
	}

	// Extract text from the now text-layered PDF
	if text := extractPDFPoppler(tmp.Name()); text != "" {
		return text
	}
	sc, err := extractPDFGo(tmp.Name())
	if err != nil {
		return ""
	}
	return sc.Text
}
