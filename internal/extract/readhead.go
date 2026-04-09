package extract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// ReadHead reads the first maxRunes runes from a file.
// For PDF files, extracts text from the first page using pdftotext (poppler)
// with fallback to the Go PDF library.
// Returns empty string on error or if file doesn't exist.
func ReadHead(path string, maxRunes int) string {
	if strings.ToLower(filepath.Ext(path)) == ".pdf" {
		return readHeadPDF(path, maxRunes)
	}
	return readHeadText(path, maxRunes)
}

// readHeadPDF extracts text from the first page of a PDF.
// Tries pdftotext (poppler) first for better font/encoding support,
// falls back to the Go PDF library if pdftotext is not available.
func readHeadPDF(path string, maxRunes int) string {
	// Try pdftotext first — handles complex font encodings (e.g. Skia/Chrome PDFs)
	if text := readHeadPDFToText(path, maxRunes); text != "" {
		return text
	}
	// Fallback to Go library
	return readHeadPDFGo(path, maxRunes)
}

// readHeadPDFToText uses pdftotext (poppler) to extract first-page text.
func readHeadPDFToText(path string, maxRunes int) string {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		return "" // pdftotext not installed
	}

	// -l 1: first page only, "-": stdout
	out, err := exec.Command(pdftotext, "-l", "1", path, "-").Output()
	if err != nil {
		return ""
	}

	text := strings.TrimSpace(string(out))
	runes := []rune(text)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

// readHeadPDFGo extracts text using the pure Go PDF library.
func readHeadPDFGo(path string, maxRunes int) string {
	f, r, err := pdf.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	if r.NumPage() < 1 {
		return ""
	}

	page := r.Page(1)
	if page.V.IsNull() {
		return ""
	}

	content, err := page.GetPlainText(nil)
	if err != nil {
		return ""
	}

	runes := []rune(content)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

// readHeadText reads the first maxRunes runes from a text file.
func readHeadText(path string, maxRunes int) string {
	maxBytes := maxRunes * 4
	if maxBytes > 8192 {
		maxBytes = 8192
	}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, maxBytes)
	n, _ := f.Read(buf)
	buf = buf[:n]

	count := 0
	i := 0
	for i < len(buf) && count < maxRunes {
		_, size := utf8.DecodeRune(buf[i:])
		i += size
		count++
	}

	return string(buf[:i])
}
