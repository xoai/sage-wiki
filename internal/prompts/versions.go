package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// templateVersions maps the compile-rendered templates to their semantic
// versions (SPEC-04 template_key). Only templates the compile pipeline
// actually renders are versioned — caption_image (image path uses an inline
// prompt, summarize.go) and capture_knowledge (MCP-only) are intentionally
// absent; editing THEM must not recompile the corpus. Bump a version when
// its template changes; the effective-content hash (EffectiveTemplateHashes)
// additionally catches edits that forgot the bump, and user overrides.
var templateVersions = map[string]string{
	"summarize_article": "1.0.0",
	"summarize_paper":   "1.0.0",
	"extract_concepts":  "1.0.0",
	"write_article":     "1.0.0",
	"extract_triples":   "1.0.0",
	"resolve_entities":  "1.0.0",
}

// TemplateVersions returns a copy of the compile-template version table.
func TemplateVersions() map[string]string {
	out := make(map[string]string, len(templateVersions))
	for k, v := range templateVersions {
		out[k] = v
	}
	return out
}

// CompileTemplateNames returns the versioned template names, sorted
// (SPEC-04 D1: the compile key joins components in canonical order).
func CompileTemplateNames() []string {
	names := make([]string, 0, len(templateVersions))
	for name := range templateVersions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// EffectiveTemplateHashes returns sha256[:16 hex] of each compile
// template's EFFECTIVE bytes — the registry's override when present, else
// the embedded default. This is the drift channel for overrides and for
// embedded edits that forgot to bump the version constant.
func EffectiveTemplateHashes(r *Registry) (map[string]string, error) {
	out := make(map[string]string, len(templateVersions))
	for _, name := range CompileTemplateNames() {
		var text string
		tmpl := r.templates.Lookup(name + ".txt")
		if tmpl == nil {
			data, err := templateFS.ReadFile("templates/" + name + ".txt")
			if err != nil {
				return nil, err
			}
			text = string(data)
		} else {
			text = tmpl.Root.String()
		}
		sum := sha256.Sum256([]byte(text))
		out[name] = hex.EncodeToString(sum[:])[:16]
	}
	return out, nil
}
