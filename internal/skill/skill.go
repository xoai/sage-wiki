package skill

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/ontology"
)

type AgentTarget string

const (
	TargetClaudeCode AgentTarget = "claude-code"
	TargetCursor     AgentTarget = "cursor"
	TargetWindsurf   AgentTarget = "windsurf"
	TargetAgentsMD   AgentTarget = "agents-md"
	TargetCodex      AgentTarget = "codex"
	TargetGemini     AgentTarget = "gemini"
	TargetGeneric    AgentTarget = "generic"
)

type TemplateData struct {
	Project           string
	SourceTypes       string
	EntityTypes       []string
	RelationTypes     []string
	HasOntology       bool
	DefaultTier       int
	HasGraphExpansion bool
}

type TargetInfo struct {
	FileName    string
	Format      string // "markdown" or "plaintext"
	StartMarker string
	EndMarker   string
}

var supportedTargets = "claude-code, cursor, windsurf, agents-md, codex, gemini, generic"

var targetRegistry = map[AgentTarget]TargetInfo{
	TargetClaudeCode: {FileName: "CLAUDE.md", Format: "markdown", StartMarker: "<!-- sage-wiki:skill:start -->", EndMarker: "<!-- sage-wiki:skill:end -->"},
	TargetCursor:     {FileName: ".cursorrules", Format: "plaintext", StartMarker: "# sage-wiki:skill:start", EndMarker: "# sage-wiki:skill:end"},
	TargetWindsurf:   {FileName: ".windsurfrules", Format: "plaintext", StartMarker: "# sage-wiki:skill:start", EndMarker: "# sage-wiki:skill:end"},
	TargetAgentsMD:   {FileName: "AGENTS.md", Format: "markdown", StartMarker: "<!-- sage-wiki:skill:start -->", EndMarker: "<!-- sage-wiki:skill:end -->"},
	TargetCodex:      {FileName: "AGENTS.md", Format: "markdown", StartMarker: "<!-- sage-wiki:skill:start -->", EndMarker: "<!-- sage-wiki:skill:end -->"},
	TargetGemini:     {FileName: "GEMINI.md", Format: "markdown", StartMarker: "<!-- sage-wiki:skill:start -->", EndMarker: "<!-- sage-wiki:skill:end -->"},
	TargetGeneric:    {FileName: "sage-wiki-skill.md", Format: "markdown", StartMarker: "<!-- sage-wiki:skill:start -->", EndMarker: "<!-- sage-wiki:skill:end -->"},
}

func TargetInfoFor(target AgentTarget) (TargetInfo, error) {
	info, ok := targetRegistry[target]
	if !ok {
		return TargetInfo{}, fmt.Errorf("unknown agent target %q; supported: %s", target, supportedTargets)
	}
	return info, nil
}

func BuildTemplateData(cfg *config.Config) TemplateData {
	seen := make(map[string]bool)
	var types []string
	for _, s := range cfg.Sources {
		t := s.Type
		if t == "" {
			t = "auto"
		}
		if !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	sort.Strings(types)

	entityDefs := ontology.MergedEntityTypes(cfg.Ontology.EntityTypes)
	entityNames := make([]string, len(entityDefs))
	for i, e := range entityDefs {
		entityNames[i] = e.Name
	}

	relationDefs := ontology.MergedRelations(cfg.Ontology.Relations)
	relationNames := make([]string, len(relationDefs))
	for i, r := range relationDefs {
		relationNames[i] = r.Name
	}

	tier := cfg.Compiler.DefaultTier
	if tier == 0 {
		tier = 3
	}

	return TemplateData{
		Project:           cfg.Project,
		SourceTypes:       strings.Join(types, ", "),
		EntityTypes:       entityNames,
		RelationTypes:     relationNames,
		HasOntology:       true,
		DefaultTier:       tier,
		HasGraphExpansion: cfg.Search.GraphExpansionEnabled(),
	}
}
