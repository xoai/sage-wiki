package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"

	wikimcp "github.com/xoai/sage-wiki/internal/mcp"
	"github.com/xoai/sage-wiki/internal/wiki"
)

type toolEntry struct {
	Name        string
	Description string
	Args        []argEntry
	REST        string
	Kind        string // "read", "write", "async"
}

type argEntry struct {
	Name     string
	Type     string
	Required bool
	Default  string
}

type skillData struct {
	ToolCount  int
	Tools      []toolEntry
	ErrorCodes []errorCodeEntry
	OptInFlags []optInFlag
	Tiers      []tierEntry
}

type errorCodeEntry struct {
	Code string
	HTTP string
	When string
}

type optInFlag struct {
	Flag    string
	Default string
	Unlocks string
}

type tierEntry struct {
	Tier  string
	Label string
	Desc  string
}

func main() {
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}

	sd := collectData(dir)

	if err := os.MkdirAll("skills/sage-wiki", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("skills/sage-wiki-integrate", 0755); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}

	refBuf := &bytes.Buffer{}
	if err := referenceTmpl.Execute(refBuf, sd); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: reference template: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile("skills/sage-wiki/SKILL.md", refBuf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}

	pipeBuf := &bytes.Buffer{}
	if err := pipelineTmpl.Execute(pipeBuf, sd); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: pipeline template: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile("skills/sage-wiki-integrate/SKILL.md", pipeBuf.Bytes(), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("skillgen: wrote skills/sage-wiki/SKILL.md (%d bytes) and skills/sage-wiki-integrate/SKILL.md (%d bytes)\n",
		refBuf.Len(), pipeBuf.Len())
}

func collectData(projectDir string) skillData {
	// Initialize a throwaway wiki to get an MCP server with the full tool
	// registry. The wiki is created in a temp dir so real projects are
	// never touched.
	td, err := os.MkdirTemp("", "skillgen-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(td)

	if err := wiki.InitGreenfield(td, "skillgen", "gemini-2.5-flash"); err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: init: %v\n", err)
		os.Exit(1)
	}

	srv, err := wikimcp.NewServer(td)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillgen: server: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	toolSet := srv.MCPServer().ListTools()

	// REST route mapping — mirrors 04-reference-rest-mcp-mapping.md.
	restMap := map[string]string{
		"wiki_search":         "GET /v1/search",
		"wiki_read":           "GET /v1/articles/{path}",
		"wiki_status":         "GET /v1/status",
		"wiki_ontology_query": "GET /v1/ontology/{entity}/traverse",
		"wiki_graph_query":    "POST /v1/graph/query",
		"wiki_list":           "GET /v1/entities",
		"wiki_provenance":     "GET /v1/provenance",
		"wiki_compile_diff":   "GET /v1/compile/diff",
		"wiki_add_source":     "POST /v1/sources",
		"wiki_write_summary":  "PUT /v1/summaries",
		"wiki_write_article":  "PUT /v1/articles/{concept}",
		"wiki_add_ontology":   "POST /v1/ontology/entities / POST /v1/ontology/relations",
		"wiki_learn":          "POST /v1/learnings",
		"wiki_commit":         "POST /v1/git/commit",
		"wiki_capture":        "POST /v1/capture",
		"wiki_compile":        "POST /v1/jobs/compile (async — 202 + job_id)",
		"wiki_compile_topic":  "POST /v1/jobs/compile?topic=... (async — 202 + job_id)",
		"wiki_lint":           "POST /v1/jobs/lint (async — 202 + job_id)",
	}

	readTools := map[string]bool{
		"wiki_search": true, "wiki_read": true, "wiki_status": true,
		"wiki_ontology_query": true, "wiki_graph_query": true,
		"wiki_list": true, "wiki_provenance": true, "wiki_compile_diff": true,
	}
	asyncTools := map[string]bool{
		"wiki_compile": true, "wiki_compile_topic": true, "wiki_lint": true,
	}

	sd := skillData{
		ToolCount: len(toolSet),
		ErrorCodes: []errorCodeEntry{
			{Code: "invalid_argument", HTTP: "400", When: "Missing, malformed, or out-of-range argument."},
			{Code: "unauthenticated", HTTP: "401", When: "Missing or invalid Bearer token."},
			{Code: "forbidden", HTTP: "403", When: "Host not allowed; path containment violation."},
			{Code: "not_found", HTTP: "404", When: "Article, entity, or job does not exist."},
			{Code: "conflict", HTTP: "409", When: "Compile already in progress; job already finished."},
			{Code: "feature_disabled", HTTP: "412", When: "`as_of` without temporal enabled; `mode=global` without communities enabled."},
			{Code: "payload_too_large", HTTP: "413", When: "Capture content over 100 KB."},
			{Code: "internal", HTTP: "500", When: "Unclassified tool failure. Message must not leak paths."},
			{Code: "unavailable", HTTP: "503", When: "Backend / store unavailable."},
		},
		OptInFlags: []optInFlag{
			{Flag: "ontology.temporal.enabled", Default: "true", Unlocks: "Historical queries (`as_of` in graph_query), temporal validity edges."},
			{Flag: "ontology.triples.enabled", Default: "false", Unlocks: "Subject-predicate-object fact extraction from articles."},
			{Flag: "ontology.resolve.enabled", Default: "false", Unlocks: "Entity resolution — merges duplicate entities across documents."},
			{Flag: "ontology.communities.enabled", Default: "false", Unlocks: "Community detection via Louvain; `mode=global` in graph_query."},
		},
		Tiers: []tierEntry{
			{Tier: "0", Label: "Index", Desc: "File metadata only — no LLM cost."},
			{Tier: "1", Label: "Embed", Desc: "Vector embeddings — no LLM summarization."},
			{Tier: "3", Label: "Full Compile", Desc: "Summarize → extract concepts → write articles. Tier 3 is ~5–8 min/doc. There is no Tier 2."},
		},
	}

	// Sorted iteration — map order is random and would break byte-identical
	// regeneration (the CI drift check diffs the output).
	names := make([]string, 0, len(toolSet))
	for name := range toolSet {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		st := toolSet[name]
		kind := "write"
		if readTools[name] {
			kind = "read"
		} else if asyncTools[name] {
			kind = "async"
		}
		rest := restMap[name]
		if rest == "" {
			rest = "—"
		}
		te := toolEntry{
			Name:        name,
			Description: firstSentence(st.Tool.Description),
			REST:        rest,
			Kind:        kind,
		}
		// Extract arguments from the tool's input schema.
		if st.Tool.InputSchema.Properties != nil {
			required := map[string]bool{}
			for _, r := range st.Tool.InputSchema.Required {
				required[r] = true
			}
			argNames := make([]string, 0, len(st.Tool.InputSchema.Properties))
			for argName := range st.Tool.InputSchema.Properties {
				argNames = append(argNames, argName)
			}
			sort.Strings(argNames)
			for _, argName := range argNames {
				propRaw := st.Tool.InputSchema.Properties[argName]
				propMap, ok := propRaw.(map[string]any)
				if !ok {
					continue
				}
				ae := argEntry{
					Name:     argName,
					Required: required[argName],
				}
				ae.Type = stringField(propMap, "type")
				if ae.Type == "" {
					ae.Type = "string"
				}
				if def, ok := propMap["default"]; ok {
					switch v := def.(type) {
					case bool:
						ae.Default = fmt.Sprintf("%v", v)
					case float64:
						ae.Default = fmt.Sprintf("%.0f", v)
					case string:
						ae.Default = v
					default:
						ae.Default = "—"
					}
				}
				if ae.Default == "" && !ae.Required {
					switch ae.Type {
					case "boolean":
						ae.Default = "false"
					default:
						ae.Default = "—"
					}
				}
				te.Args = append(te.Args, ae)
			}
		}
		sd.Tools = append(sd.Tools, te)
	}
	return sd
}

func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	idx := strings.IndexAny(s, ".\n")
	if idx == -1 {
		return s
	}
	if idx < len(s)-1 && s[idx] == '.' && s[idx+1] != ' ' {
		// Period in middle of a word, e.g. "v0.2.5" — skip.
		for i := idx + 1; i < len(s); i++ {
			if s[i] == ' ' || s[i] == '\n' {
				return strings.TrimSpace(s[:i+1])
			}
		}
	}
	return strings.TrimSpace(s[:idx+1])
}

var referenceTmpl = template.Must(template.New("reference").Parse(refTemplate))
var pipelineTmpl = template.Must(template.New("pipeline").Parse(pipeTemplate))
