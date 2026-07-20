package extract

import (
	"archive/zip"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// extractEpub extracts text from an EPUB file (ZIP containing XHTML chapters).
func extractEpub(path string) (*SourceContent, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("extract epub: %w", err)
	}
	defer r.Close()

	var text strings.Builder
	budget := newZipBudget()
	chapterNum := 0

	for _, f := range r.File {
		ext := strings.ToLower(filepath.Ext(f.Name))
		if ext != ".xhtml" && ext != ".html" && ext != ".htm" {
			continue
		}

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
			chapterNum++
			text.WriteString(fmt.Sprintf("\n--- Chapter %d (%s) ---\n", chapterNum, filepath.Base(f.Name)))
			text.WriteString(content)
			text.WriteString("\n")
		}
	}

	extracted := strings.TrimSpace(text.String())
	if extracted == "" {
		return nil, fmt.Errorf("extract epub: no text content in %s", filepath.Base(path))
	}

	return &SourceContent{
		Path: path,
		Type: "article",
		Text: extracted,
	}, nil
}
