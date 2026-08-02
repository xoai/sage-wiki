package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
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
// template's EFFECTIVE RAW BYTES — the override file's bytes when one
// backs the template, else the embedded file's bytes (SPEC-04). Raw bytes,
// not the parse tree: a comment-only edit drifts (as it should — comments
// steer the model too), and a user can reproduce the printed hash with
// sha256sum on their override file.
func EffectiveTemplateHashes(r *Registry) (map[string]string, error) {
	if r == nil {
		r = defaultRegistry
	}
	out := make(map[string]string, len(templateVersions))
	for _, name := range CompileTemplateNames() {
		var data []byte
		var err error
		if path := r.OverridePath(name + ".txt"); path != "" {
			data, err = os.ReadFile(path)
		} else {
			data, err = templateFS.ReadFile("templates/" + name + ".txt")
		}
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out[name] = hex.EncodeToString(sum[:])[:16]
	}
	return out, nil
}

// DefaultRegistry exposes the package-level registry (with any prompts/
// overrides loaded through the package-level LoadFromDir) for callers that
// must compute effective-template state without holding a *Registry
// (SPEC-04's CLI compile path).
func DefaultRegistry() *Registry { return defaultRegistry }
