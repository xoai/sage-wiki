package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestTargetInfoFor(t *testing.T) {
	tests := []struct {
		target   AgentTarget
		wantFile string
		wantFmt  string
		wantErr  bool
	}{
		{TargetClaudeCode, "CLAUDE.md", "markdown", false},
		{TargetCursor, ".cursorrules", "plaintext", false},
		{TargetWindsurf, ".windsurfrules", "plaintext", false},
		{TargetAgentsMD, "AGENTS.md", "markdown", false},
		{TargetCodex, "AGENTS.md", "markdown", false},
		{TargetGemini, "GEMINI.md", "markdown", false},
		{TargetGeneric, "sage-wiki-skill.md", "markdown", false},
		{"invalid", "", "", true},
	}

	for _, tt := range tests {
		t.Run(string(tt.target), func(t *testing.T) {
			info, err := TargetInfoFor(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error for unknown target")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.FileName != tt.wantFile {
				t.Errorf("FileName = %q, want %q", info.FileName, tt.wantFile)
			}
			if info.Format != tt.wantFmt {
				t.Errorf("Format = %q, want %q", info.Format, tt.wantFmt)
			}
		})
	}
}

func TestTargetInfoFor_CodexMatchesAgentsMD(t *testing.T) {
	codex, _ := TargetInfoFor(TargetCodex)
	agentsMD, _ := TargetInfoFor(TargetAgentsMD)
	if codex.FileName != agentsMD.FileName {
		t.Errorf("codex FileName %q != agents-md FileName %q", codex.FileName, agentsMD.FileName)
	}
	if codex.Format != agentsMD.Format {
		t.Errorf("codex Format %q != agents-md Format %q", codex.Format, agentsMD.Format)
	}
}

func TestTargetInfoFor_MarkerStyles(t *testing.T) {
	md, _ := TargetInfoFor(TargetClaudeCode)
	if md.StartMarker != "<!-- sage-wiki:skill:start -->" {
		t.Errorf("markdown StartMarker = %q", md.StartMarker)
	}
	pt, _ := TargetInfoFor(TargetCursor)
	if pt.StartMarker != "# sage-wiki:skill:start" {
		t.Errorf("plaintext StartMarker = %q", pt.StartMarker)
	}
}

func TestBuildTemplateData(t *testing.T) {
	cfg := &config.Config{
		Project: "test-project",
		Sources: []config.Source{
			{Path: "src", Type: "code"},
			{Path: "docs", Type: "article"},
			{Path: "lib", Type: "code"},
		},
		Ontology: config.OntologyConfig{
			EntityTypes: []config.EntityTypeConfig{
				{Name: "decision", Description: "Architectural decision"},
			},
		},
		Compiler: config.CompilerConfig{
			DefaultTier: 2,
		},
	}

	data := BuildTemplateData(cfg)

	if data.Project != "test-project" {
		t.Errorf("Project = %q", data.Project)
	}
	if data.SourceTypes != "article, code" {
		t.Errorf("SourceTypes = %q, want %q", data.SourceTypes, "article, code")
	}
	if data.DefaultTier != 2 {
		t.Errorf("DefaultTier = %d, want 2", data.DefaultTier)
	}
	if !data.HasOntology {
		t.Error("HasOntology should be true")
	}
	if !data.HasGraphExpansion {
		t.Error("HasGraphExpansion should default to true")
	}

	found := false
	for _, e := range data.EntityTypes {
		if e == "decision" {
			found = true
		}
	}
	if !found {
		t.Errorf("EntityTypes should include custom 'decision', got %v", data.EntityTypes)
	}

	foundConcept := false
	for _, e := range data.EntityTypes {
		if e == "concept" {
			foundConcept = true
		}
	}
	if !foundConcept {
		t.Errorf("EntityTypes should include built-in 'concept', got %v", data.EntityTypes)
	}

	foundRel := false
	for _, r := range data.RelationTypes {
		if r == "implements" {
			foundRel = true
		}
	}
	if !foundRel {
		t.Errorf("RelationTypes should include built-in 'implements', got %v", data.RelationTypes)
	}
}

func TestBuildTemplateData_DefaultTier(t *testing.T) {
	cfg := &config.Config{Project: "x"}
	data := BuildTemplateData(cfg)
	if data.DefaultTier != 3 {
		t.Errorf("DefaultTier should default to 3, got %d", data.DefaultTier)
	}
}

func sampleData() TemplateData {
	return TemplateData{
		Project:           "myapp",
		SourceTypes:       "article, code",
		EntityTypes:       []string{"concept", "technique", "decision"},
		RelationTypes:     []string{"implements", "extends", "contradicts"},
		HasOntology:       true,
		DefaultTier:       3,
		HasGraphExpansion: true,
	}
}

func TestRenderSkill(t *testing.T) {
	data := sampleData()
	out, err := RenderSkill(data)
	if err != nil {
		t.Fatalf("RenderSkill error: %v", err)
	}
	if strings.Contains(out, "{{") {
		t.Error("output contains unresolved template syntax")
	}
	if !strings.Contains(out, "myapp") {
		t.Error("output should contain project name")
	}
	if !strings.Contains(out, "wiki_search") {
		t.Error("output should reference wiki_search tool")
	}
	if !strings.Contains(out, "wiki_learn") {
		t.Error("output should reference wiki_learn tool")
	}
	if !strings.Contains(out, "decision") {
		t.Error("output should contain entity type 'decision'")
	}
	if !strings.Contains(out, "implements") {
		t.Error("output should contain relation type 'implements'")
	}
}

func TestWriteSkill_NewFile(t *testing.T) {
	dir := t.TempDir()
	data := sampleData()

	err := WriteSkill(dir, TargetClaudeCode, data)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	s := string(content)
	if !strings.Contains(s, "<!-- sage-wiki:skill:start -->") {
		t.Error("missing start marker")
	}
	if !strings.Contains(s, "<!-- sage-wiki:skill:end -->") {
		t.Error("missing end marker")
	}
	if !strings.Contains(s, "myapp") {
		t.Error("missing project name in output")
	}
}

func TestWriteSkill_AppendToExisting(t *testing.T) {
	dir := t.TempDir()
	existing := "# My Project\n\nExisting content here.\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644)

	data := sampleData()
	err := WriteSkill(dir, TargetClaudeCode, data)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	s := string(content)
	if !strings.HasPrefix(s, "# My Project") {
		t.Error("existing content should be preserved at the start")
	}
	if !strings.Contains(s, "<!-- sage-wiki:skill:start -->") {
		t.Error("markers should be appended")
	}
}

func TestWriteSkill_RefreshMarkers(t *testing.T) {
	dir := t.TempDir()
	initial := "# Header\n\n<!-- sage-wiki:skill:start -->\nOLD CONTENT\n<!-- sage-wiki:skill:end -->\n\n# Footer\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(initial), 0644)

	data := sampleData()
	err := WriteSkill(dir, TargetClaudeCode, data)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	s := string(content)
	if strings.Contains(s, "OLD CONTENT") {
		t.Error("old content between markers should be replaced")
	}
	if !strings.Contains(s, "# Header") {
		t.Error("content before markers should be preserved")
	}
	if !strings.Contains(s, "# Footer") {
		t.Error("content after markers should be preserved")
	}
	if !strings.Contains(s, "myapp") {
		t.Error("new content should be present")
	}
}

func TestWriteSkill_MalformedMarkers(t *testing.T) {
	dir := t.TempDir()
	initial := "# Header\n\n<!-- sage-wiki:skill:start -->\nORPHAN\n"
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(initial), 0644)

	data := sampleData()
	err := WriteSkill(dir, TargetClaudeCode, data)
	if err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	s := string(content)
	count := strings.Count(s, "<!-- sage-wiki:skill:start -->")
	if count < 2 {
		t.Errorf("expected at least 2 start markers (orphan + new), got %d", count)
	}
}

func TestFormatForTarget_Plaintext(t *testing.T) {
	data := sampleData()
	out, _ := RenderSkill(data)
	formatted := FormatForTarget(out, TargetCursor)
	if strings.Contains(formatted, "## ") || strings.Contains(formatted, "### ") {
		t.Error("plaintext output should not contain markdown headers")
	}
	if !strings.Contains(formatted, "---") {
		t.Error("plaintext should have section separators")
	}
}

func TestPreviewSkill(t *testing.T) {
	dir := t.TempDir()
	data := sampleData()

	out, err := PreviewSkill(TargetClaudeCode, data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "myapp") {
		t.Error("preview should contain rendered content")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("preview should not create any files")
	}
}
