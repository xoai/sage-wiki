package parity

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
)

// NewOriginServer is the scripted LLM origin (SPEC-09 §2.3): a canned
// OpenAI-compatible server producing deterministic, content-classified
// responses for the record flow. Deterministic per (class, source): the
// same corpus always yields the same fixtures.
func NewOriginServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(originHandler))
}

func originHandler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/embeddings"):
		handleOriginEmbeddings(w, r)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		handleOriginChat(w, r)
	case strings.HasSuffix(r.URL.Path, "/models"):
		w.Write([]byte(`{"data":[{"id":"gpt-4o-mini","object":"model"}]}`))
	default:
		http.NotFound(w, r)
	}
}

type originChatReq struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func handleOriginChat(w http.ResponseWriter, r *http.Request) {
	var req originChatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	last := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	content := originClassify(last)
	usage := map[string]int{
		"prompt_tokens":     len(last) / 4,
		"completion_tokens": len(content) / 4,
		"total_tokens":      (len(last) + len(content)) / 4,
	}
	writeJSON(w, map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": content}, "finish_reason": "stop"}},
		"model":   req.Model,
		"usage":   usage,
	})
}

// originClassify produces the deterministic per-class reply. The classes
// mirror the compiler's prompts (summarize / concept extraction / article
// writing / query synthesis); each reply embeds a stable marker of the
// input so golden content is corpus-derived, not constant.
func originClassify(userMsg string) string {
	marker := markerOf(userMsg)
	if strings.Contains(userMsg, "Graph edges:") {
		// graphqa structured output (schema: answer + cited edge indices).
		return `{"answer": "The graph shows the documented relationships for this topic, grounded in the compiled articles.", "cited": [1]}`
	}
	if strings.Contains(userMsg, "## Relations") && strings.Contains(userMsg, "untrusted_source") {
		return originTriples(userMsg)
	}
	switch {
	case strings.Contains(userMsg, "concept extraction system"):
		// One concept per source marker in the batch (up to 5) so the
		// golden corpus builds a real graph.
		markers := allMarkers(userMsg, 12)
		var parts []string
		for _, m := range markers {
			parts = append(parts, `{"name": "`+m.slug+`", "aliases": [], "sources": ["`+m.src+`"], "type": "concept"}`)
		}
		return `[` + strings.Join(parts, ", ") + `]`
	case strings.Contains(userMsg, "wiki author writing a comprehensive article"):
		return "---\nconcept: " + marker + "\n---\n\n# " + titleCase(marker) + "\n\n" +
			titleCase(marker) + " is a documented concept in this workspace. It relates to its source material directly.\n\n## See also\n\n- Related concepts appear across the corpus."
	case strings.Contains(userMsg, "You are a knowledge base Q&A assistant"):
		return "Based on the wiki, " + marker + " is covered in the compiled articles. See [[" + marker + "]]."
	default:
		// Summarize: ≥100 chars to pass quality validation. For the docs
		// originTriples emits edges for, the body embeds the evidence
		// sentence(s) VERBATIM so SPEC-08 span verification grounds every
		// golden edge (the triples pass quotes the compiled summary).
		return "## Key claims\n\n" + summaryBodyFor(sourceOf(userMsg), marker) + "\n\n## Concepts\n\n" + marker + ": The central concept developed by this document."
	}
}

// Evidence sentences quoted by originTriples. The summarize pass embeds them
// verbatim (summaryBodyFor) so span verification grounds each golden edge.
const (
	evAlphaBeta = "The Alpha protocol establishes sessions via the Beta Handshake."
	evBetaGamma = "It delegates key confirmation to the Gamma Proof step."
	evFactV1    = "Project Lighthouse uses a centralized coordinator for all compile jobs."
	evFactV2a   = "Project Lighthouse moved to a decentralized lease model in early 2025."
	evFactV2b   = "the centralized coordinator was removed."
	evK8sEtcd   = "K8s uses etcd for cluster state."
)

// summaryBodyFor returns the summarize-body text for a source. Docs that
// originTriples emits edges for carry their evidence sentence(s) verbatim;
// every other doc gets the generic claim. The generic lead keeps all
// summaries ≥100 chars (quality validation).
func summaryBodyFor(src, marker string) string {
	generic := titleCase(marker) + " is the central subject of this source document, which lays out its definition, mechanics, and practical implications in detail."
	switch {
	case strings.Contains(src, "a-links-b"):
		return generic + " " + evAlphaBeta
	case strings.Contains(src, "b-links-c"):
		return generic + " " + evBetaGamma
	case strings.Contains(src, "fact-v1"):
		return generic + " " + evFactV1
	case strings.Contains(src, "fact-v2"):
		return generic + " " + evFactV2a + " " + evFactV2b
	case strings.Contains(src, "kubernetes-k8s"):
		return generic + " " + evK8sEtcd
	default:
		return generic
	}
}

// markerOf extracts a stable slug from the prompt: the first ### Source:
// path if present, else the summarize template's Source file: path, else the
// first heading-ish token.
func markerOf(s string) string {
	if i := strings.Index(s, "### Source: "); i != -1 {
		rest := s[i+len("### Source: "):]
		if j := strings.Index(rest, "\n"); j != -1 {
			rest = rest[:j]
		}
		return slugify(rest)
	}
	if i := strings.Index(s, "Source file: "); i != -1 {
		rest := s[i+len("Source file: "):]
		if j := strings.Index(rest, "\n"); j != -1 {
			rest = rest[:j]
		}
		return slugify(rest)
	}
	if i := strings.Index(s, "Source: "); i != -1 {
		rest := s[i+len("Source: "):]
		if j := strings.Index(rest, "\n"); j != -1 {
			rest = rest[:j]
		}
		return slugify(rest)
	}
	fields := strings.Fields(s)
	if len(fields) > 0 {
		return slugify(fields[0])
	}
	return "doc"
}

// sourceOf extracts the raw source path for concept JSON.
func sourceOf(s string) string {
	if i := strings.Index(s, "### Source: "); i != -1 {
		rest := s[i+len("### Source: "):]
		if j := strings.Index(rest, "\n"); j != -1 {
			return strings.TrimSpace(rest[:j])
		}
	}
	if i := strings.Index(s, "Source file: "); i != -1 {
		rest := s[i+len("Source file: "):]
		if j := strings.Index(rest, "\n"); j != -1 {
			return strings.TrimSpace(rest[:j])
		}
	}
	return "raw/unknown.md"
}

type srcMarker struct{ slug, src string }

// allMarkers extracts every "### Source:" marker in a batch, capped.
// Concept names come from the BASENAME (semantic, distinct) — a shared
// prefix would make the embedding dedup merge them all into a few.
func allMarkers(s string, limit int) []srcMarker {
	var out []srcMarker
	rest := s
	for len(out) < limit {
		i := strings.Index(rest, "### Source: ")
		if i == -1 {
			break
		}
		rest = rest[i+len("### Source: "):]
		j := strings.Index(rest, "\n")
		if j == -1 {
			break
		}
		src := strings.TrimSpace(rest[:j])
		base := src
		if k := strings.LastIndex(src, "/"); k != -1 {
			base = src[k+1:]
		}
		out = append(out, srcMarker{slug: slugify(base), src: src})
		rest = rest[j:]
	}
	if len(out) == 0 {
		out = append(out, srcMarker{slug: "doc", src: "raw/unknown.md"})
	}
	return out
}

func slugify(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSuffix(s, ".md")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' || r == '/' {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "doc"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func titleCase(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

type originEmbedReq struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string or []string
}

func handleOriginEmbeddings(w http.ResponseWriter, r *http.Request) {
	var req originEmbedReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var texts []string
	switch v := req.Input.(type) {
	case string:
		texts = []string{v}
	case []any:
		for _, t := range v {
			if s, ok := t.(string); ok {
				texts = append(texts, s)
			}
		}
	}
	data := make([]map[string]any, len(texts))
	for i, text := range texts {
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": fnvVec(text, 8)}
	}
	writeJSON(w, map[string]any{"object": "list", "data": data, "model": req.Model})
}

// originTriples produces evidenced graph output for the triples pass:
// multihop chains, the contradiction pair (with a contradicts edge so the
// golden exercises temporal/supersession semantics), and the k8s alias.
func originTriples(userMsg string) string {
	src := sourceOf(userMsg)
	ent := func(name, typ, desc string) string {
		return fmt.Sprintf(`{"name": %q, "type": %q, "description": %q}`, name, typ, desc)
	}
	rel := func(source, pred, target, evidence string, conf float64) string {
		return fmt.Sprintf(`{"source": %q, "predicate": %q, "target": %q, "evidence": %q, "confidence": %.2f}`, source, pred, target, evidence, conf)
	}
	switch {
	case strings.Contains(src, "a-links-b"):
		return `{"entities": [` + ent("alpha-protocol", "concept", "A session protocol gated on the Beta Handshake.") + `,` +
			ent("beta-handshake", "concept", "The cipher and session-key negotiation step Alpha requires.") + `],"relations": [` +
			rel("alpha-protocol", "prerequisite", "beta-handshake", evAlphaBeta, 0.90) + `]}`
	case strings.Contains(src, "b-links-c"):
		return `{"entities": [` + ent("beta-handshake", "concept", "The cipher and session-key negotiation step.") + `,` +
			ent("gamma-proof", "concept", "The zero-knowledge key confirmation completing the handshake.") + `],"relations": [` +
			rel("beta-handshake", "prerequisite", "gamma-proof", evBetaGamma, 0.90) + `]}`
	case strings.Contains(src, "c-terminal"):
		return `{"entities": [` + ent("gamma-proof", "concept", "The terminal zero-knowledge confirmation of the chain.") + `],"relations": []}`
	case strings.Contains(src, "fact-v1"):
		return `{"entities": [` + ent("project-lighthouse", "concept", "The compile-job orchestration project.") + `,` +
			ent("centralized-coordinator", "concept", "The mid-2024 centralized job coordinator.") + `],"relations": [` +
			rel("project-lighthouse", "implements", "centralized-coordinator", evFactV1, 0.90) + `]}`
	case strings.Contains(src, "fact-v2"):
		return `{"entities": [` + ent("project-lighthouse", "concept", "The compile-job orchestration project.") + `,` +
			ent("decentralized-lease", "concept", "The early-2025 decentralized lease claiming model.") + `,` +
			ent("centralized-coordinator", "concept", "The removed mid-2024 coordinator.") + `],"relations": [` +
			rel("project-lighthouse", "implements", "decentralized-lease", evFactV2a, 0.95) + `,` +
			rel("decentralized-lease", "contradicts", "centralized-coordinator", evFactV2b, 0.95) + `]}`
	case strings.Contains(src, "kubernetes-k8s"):
		return `{"entities": [` + ent("kubernetes", "concept", "The container orchestration system (alias K8s).") + `,` +
			ent("etcd", "concept", "The cluster-state store Kubernetes uses.") + `],"relations": [` +
			rel("kubernetes", "implements", "etcd", evK8sEtcd, 0.85) + `]}`
	default:
		return `{"entities": [], "relations": []}`
	}
}

// fnvVec is the deterministic content-hash embedding (providerfake scheme).
func fnvVec(text string, dims int) []float32 {
	vec := make([]float32, dims)
	for d := 0; d < dims; d++ {
		h := fnv.New32a()
		h.Write([]byte(text))
		h.Write([]byte{byte(d)})
		vec[d] = float32(h.Sum32()%1000) / 1000.0
	}
	return vec
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
