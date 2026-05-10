package skill

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed packs/*.md.tmpl
var packFS embed.FS

const baseTemplate = "packs/base.md.tmpl"

func RenderSkill(data TemplateData) (string, error) {
	content, err := packFS.ReadFile(baseTemplate)
	if err != nil {
		return "", fmt.Errorf("read base skill template: %w", err)
	}

	tmpl, err := template.New(baseTemplate).Parse(string(content))
	if err != nil {
		return "", fmt.Errorf("parse base skill template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute base skill template: %w", err)
	}

	return buf.String(), nil
}
