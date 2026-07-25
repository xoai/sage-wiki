package extract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/xoai/sage-wiki/internal/log"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, pdftotext, path, "-").Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

// extractPDFGo uses the pure Go PDF library.
// extractPDFGo recovers library panics into errors (P2-5 fuzz finding:
// the ledongthuc/pdf library panics on malformed input in many shapes —
// "malformed PDF", "unexpected keyword", EOF — so message-matching is
// fragile). All panics are recovered AND logged at Warn (nothing is
// silent; a bug in our own code remains visible in logs).
func extractPDFGo(path string) (_ *SourceContent, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("extract pdf: recovered library panic", "path", path, "panic", r)
			err = fmt.Errorf("extract pdf: malformed input (library panic): %v", r)
		}
	}()

	// Open ourselves and pass the reader: pdf.Open can PANIC internally
	// after os.Open succeeds — then our defer never ran and the handle
	// leaked (Windows TempDir cleanup fails on the open handle).
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("extract pdf: open: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("extract pdf: stat: %w", err)
	}
	r, err := pdf.NewReader(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("extract pdf: open: %w", err)
	}

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
