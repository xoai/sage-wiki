// Package capturefmt owns the ONE capture-file frontmatter format
// (pack rule 2): engine.Capture, the CLI, and the MCP capture paths all
// build through here so the format can never diverge.
package capturefmt

import (
	"fmt"
	"strings"
)

// Frontmatter renders the capture frontmatter: source/captured_at, any
// extra keys (e.g. "raw: true"), then tags and context, then the closing
// fence. Field order is part of the format. Tags must be a comma-separated
// list — a tag containing ']' or a newline would break the YAML, so it is
// rejected rather than quoted (capture tags are simple labels by design).
func Frontmatter(origin, now, tags, context string, extraKeys ...string) (string, error) {
	if strings.ContainsAny(origin, "\n\r") {
		return "", fmt.Errorf("capture origin must be a single line")
	}
	for _, tag := range strings.Split(tags, ",") {
		tag = strings.TrimSpace(tag)
		if strings.ContainsAny(tag, "]\n\r") {
			return "", fmt.Errorf("capture tag %q would break the YAML frontmatter (no ']' or newlines allowed)", tag)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\nsource: %s\ncaptured_at: %s\n", origin, now)
	for _, k := range extraKeys {
		b.WriteString(k)
		b.WriteString("\n")
	}
	if tags != "" {
		fmt.Fprintf(&b, "tags: [%s]\n", tags)
	}
	if context != "" {
		fmt.Fprintf(&b, "context: %q\n", context)
	}
	b.WriteString("---\n\n")
	return b.String(), nil
}
