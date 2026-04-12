package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PreExtractFrontmatter holds parsed frontmatter from pre-extracted files.
type PreExtractFrontmatter struct {
	PreExtracted bool   `yaml:"pre_extracted"`
	Confidence   string `yaml:"confidence"`
	Engine       string `yaml:"engine"`
	OriginalPath string `yaml:"original_path"`
	OriginalHash string `yaml:"original_hash"`
}

// TryPreExtracted checks for a pre-extracted .md file and returns its content.
// Returns nil, nil if no pre-extracted file exists or confidence is low.
func TryPreExtracted(projectDir string, rawRelPath string) (*SourceContent, error) {
	// raw/inbox/test.pdf → inbox/test.pdf
	relPath := rawRelPath
	if strings.HasPrefix(relPath, "raw/") {
		relPath = relPath[4:]
	}

	preDir := filepath.Join(projectDir, ".pre-extracted", "files")
	mdPath := filepath.Join(preDir, relPath+".md")

	data, err := os.ReadFile(mdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// 解析 frontmatter
	content := string(data)
	fm, body, err := parsePreExtractFrontmatter(content)
	if err != nil {
		return nil, nil // 损坏的 frontmatter，降级到 Go 引擎
	}

	if !fm.PreExtracted || fm.Confidence == "low" {
		return nil, nil
	}

	sc := &SourceContent{
		Path:          rawRelPath,
		Text:          body,
		PreExtracted:  true,
		Confidence:    fm.Confidence,
		ExtractEngine: fm.Engine,
	}
	return sc, nil
}

// ExtractFromProject tries pre-extracted content first, then falls back to the Go engine.
func ExtractFromProject(projectDir string, relPath string, sourceType string) (*SourceContent, error) {
	sc, err := TryPreExtracted(projectDir, relPath)
	if err != nil {
		return nil, fmt.Errorf("pre-extract check: %w", err)
	}
	if sc != nil {
		return sc, nil
	}

	// 降级到原有 Go 引擎
	absPath := filepath.Join(projectDir, relPath)
	return Extract(absPath, sourceType)
}

func parsePreExtractFrontmatter(content string) (*PreExtractFrontmatter, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, content, fmt.Errorf("no frontmatter")
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, content, fmt.Errorf("no frontmatter end")
	}
	fmStr := content[4 : 4+end]
	body := strings.TrimSpace(content[4+end+4:])

	var fm PreExtractFrontmatter
	if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
		return nil, content, err
	}
	return &fm, body, nil
}
