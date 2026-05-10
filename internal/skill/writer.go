package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xoai/sage-wiki/internal/log"
)

var mdHeaderRe = regexp.MustCompile(`(?m)^(#{2,3}) (.+)$`)

func FormatForTarget(content string, target AgentTarget) string {
	info, err := TargetInfoFor(target)
	if err != nil || info.Format == "markdown" {
		return content
	}
	return mdHeaderRe.ReplaceAllStringFunc(content, func(match string) string {
		parts := mdHeaderRe.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		return "--- " + parts[2] + " ---"
	})
}

func WriteSkill(projectDir string, target AgentTarget, data TemplateData) error {
	info, err := TargetInfoFor(target)
	if err != nil {
		return err
	}

	rendered, err := RenderSkill(data)
	if err != nil {
		return err
	}

	formatted := FormatForTarget(rendered, target)
	markedContent := info.StartMarker + "\n" + formatted + "\n" + info.EndMarker

	filePath := filepath.Join(projectDir, info.FileName)

	existing, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.WriteFile(filePath, []byte(markedContent+"\n"), 0644)
		}
		return fmt.Errorf("read %s: %w", filePath, err)
	}

	content := string(existing)
	startIdx := strings.Index(content, info.StartMarker)
	endIdx := strings.Index(content, info.EndMarker)

	if startIdx >= 0 && endIdx > startIdx {
		before := content[:startIdx]
		after := content[endIdx+len(info.EndMarker):]
		return os.WriteFile(filePath, []byte(before+markedContent+after), 0644)
	}

	if startIdx >= 0 && endIdx < 0 {
		log.Warn("malformed skill markers (start without end), appending new section", "file", filePath)
	}

	return os.WriteFile(filePath, []byte(content+"\n\n"+markedContent+"\n"), 0644)
}

func PreviewSkill(target AgentTarget, data TemplateData) (string, error) {
	rendered, err := RenderSkill(data)
	if err != nil {
		return "", err
	}
	return FormatForTarget(rendered, target), nil
}
