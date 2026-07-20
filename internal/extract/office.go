package extract

import (
	"archive/zip"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// extractDocx extracts text from a .docx file (ZIP containing word/document.xml).
func extractDocx(path string) (*SourceContent, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("extract docx: %w", err)
	}
	defer r.Close()

	var text strings.Builder
	budget := newZipBudget()
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, br, err := budget.open(path, f)
			if err != nil {
				var zle *ZipLimitError
				if errors.As(err, &zle) {
					return nil, err
				}
				return nil, fmt.Errorf("extract docx: open document.xml: %w", err)
			}
			content := extractXMLText(br)
			rc.Close()
			// The exceeded flag — not the decoder's error return — is the
			// overflow signal: partial text from a capped entry is discarded.
			if br.exceeded {
				return nil, budget.limitError(path, f.Name)
			}
			budget.charge(int64(f.UncompressedSize64), br.n)
			text.WriteString(content)
		}
	}

	extracted := strings.TrimSpace(text.String())
	if extracted == "" {
		return nil, fmt.Errorf("extract docx: no text content in %s", filepath.Base(path))
	}

	return &SourceContent{
		Path: path,
		Type: "article",
		Text: extracted,
	}, nil
}

// extractXlsx extracts text from a .xlsx file (ZIP containing xl/sharedStrings.xml + sheets).
func extractXlsx(path string) (*SourceContent, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("extract xlsx: %w", err)
	}
	defer r.Close()

	var text strings.Builder
	budget := newZipBudget() // ONE budget spans BOTH loops below

	// Extract shared strings (most cell values are here)
	for _, f := range r.File {
		if f.Name == "xl/sharedStrings.xml" {
			rc, br, err := budget.open(path, f)
			if err != nil {
				var zle *ZipLimitError
				if errors.As(err, &zle) {
					return nil, err
				}
				continue
			}
			content := extractXMLText(br)
			rc.Close()
			if br.exceeded {
				return nil, budget.limitError(path, f.Name)
			}
			budget.charge(int64(f.UncompressedSize64), br.n)
			text.WriteString(content)
			text.WriteString("\n")
		}
	}

	// Also extract from sheet data
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			rc, br, err := budget.open(path, f)
			if err != nil {
				var zle *ZipLimitError
				if errors.As(err, &zle) {
					return nil, err
				}
				continue
			}
			content := extractXMLText(br)
			rc.Close()
			if br.exceeded {
				return nil, budget.limitError(path, f.Name)
			}
			budget.charge(int64(f.UncompressedSize64), br.n)
			if content != "" {
				text.WriteString("\n--- Sheet: ")
				text.WriteString(f.Name)
				text.WriteString(" ---\n")
				text.WriteString(content)
			}
		}
	}

	extracted := strings.TrimSpace(text.String())
	if extracted == "" {
		return nil, fmt.Errorf("extract xlsx: no text content in %s", filepath.Base(path))
	}

	return &SourceContent{
		Path: path,
		Type: "dataset",
		Text: extracted,
	}, nil
}

// extractPptx extracts text from a .pptx file (ZIP containing ppt/slides/slide*.xml).
func extractPptx(path string) (*SourceContent, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("extract pptx: %w", err)
	}
	defer r.Close()

	var text strings.Builder
	budget := newZipBudget()
	slideNum := 0

	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			rc, br, err := budget.open(path, f)
			if err != nil {
				var zle *ZipLimitError
				if errors.As(err, &zle) {
					return nil, err
				}
				continue
			}
			content := extractXMLText(br)
			rc.Close()
			if br.exceeded {
				return nil, budget.limitError(path, f.Name)
			}
			budget.charge(int64(f.UncompressedSize64), br.n)
			if content != "" {
				slideNum++
				text.WriteString(fmt.Sprintf("\n--- Slide %d ---\n", slideNum))
				text.WriteString(content)
				text.WriteString("\n")
			}
		}
	}

	extracted := strings.TrimSpace(text.String())
	if extracted == "" {
		return nil, fmt.Errorf("extract pptx: no text content in %s", filepath.Base(path))
	}

	return &SourceContent{
		Path: path,
		Type: "article",
		Text: extracted,
	}, nil
}

// extractCSV reads a CSV file and formats as readable text.
func extractCSV(path string) (*SourceContent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("extract csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // variable fields

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("extract csv: %w", err)
	}

	var text strings.Builder

	// Format as readable table
	for i, row := range records {
		if i == 0 {
			text.WriteString("Headers: ")
			text.WriteString(strings.Join(row, " | "))
			text.WriteString("\n\n")
			continue
		}
		text.WriteString(strings.Join(row, " | "))
		text.WriteString("\n")

		// Limit to first 1000 rows for very large CSVs
		if i >= 1000 {
			text.WriteString(fmt.Sprintf("\n... (%d more rows truncated)\n", len(records)-1000))
			break
		}
	}

	return &SourceContent{
		Path: path,
		Type: "dataset",
		Text: strings.TrimSpace(text.String()),
	}, nil
}

// extractXMLText walks an XML stream and collects all text content.
// Works for docx (word/document.xml), pptx (slides), xlsx (sharedStrings).
func extractXMLText(r io.Reader) string {
	decoder := xml.NewDecoder(r)
	var text strings.Builder
	var inText bool

	for {
		tok, err := decoder.Token()
		if err != nil {
			break
		}

		switch t := tok.(type) {
		case xml.StartElement:
			// Collect text from <t>, <a:t>, <si>/<t> elements
			if t.Name.Local == "t" {
				inText = true
			}
			// Paragraph break
			if t.Name.Local == "p" || t.Name.Local == "br" {
				text.WriteString("\n")
			}
		case xml.CharData:
			if inText {
				text.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
				text.WriteString(" ")
			}
		}
	}

	return strings.TrimSpace(text.String())
}
