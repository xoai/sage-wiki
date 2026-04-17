package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunCompile_RejectsBatchWatch(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("watch", false, "")
	cmd.Flags().Bool("batch", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("fresh", false, "")
	cmd.Flags().Bool("re-embed", false, "")
	cmd.Flags().Bool("re-extract", false, "")
	cmd.Flags().Bool("estimate", false, "")
	cmd.Flags().Bool("no-cache", false, "")
	cmd.Flags().Bool("prune", false, "")
	cmd.Flags().StringP("dir", "d", ".", "")
	cmd.Flags().StringP("output", "o", "", "")

	cmd.Flags().Set("watch", "true")
	cmd.Flags().Set("batch", "true")

	err := runCompile(cmd, []string{})
	if err == nil {
		t.Fatal("expected error when --batch and --watch are both set")
	}
	if !strings.Contains(err.Error(), "batch") || !strings.Contains(err.Error(), "watch") {
		t.Errorf("error should mention both 'batch' and 'watch', got: %s", err.Error())
	}
}

func TestRunInit_WithSkillFlag(t *testing.T) {
	dir := t.TempDir()
	oldProjectDir := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = oldProjectDir })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("vault", false, "")
	cmd.Flags().Bool("prompts", false, "")
	cmd.Flags().String("model", "gemini-2.5-flash", "")
	cmd.Flags().String("skill", "", "")
	cmd.Flags().String("pack", "", "")
	cmd.Flags().Set("skill", "claude-code")

	err := runInit(cmd, []string{})
	if err != nil {
		t.Fatalf("runInit with --skill: %v", err)
	}

	claudeMD, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal("CLAUDE.md should exist after init --skill claude-code")
	}
	s := string(claudeMD)
	if !strings.Contains(s, "<!-- sage-wiki:skill:start -->") {
		t.Error("CLAUDE.md should contain skill markers")
	}
	if !strings.Contains(s, "sage-wiki") {
		t.Error("CLAUDE.md should contain sage-wiki skill content")
	}
}

func TestRunInit_InvalidSkillTarget(t *testing.T) {
	dir := t.TempDir()
	oldProjectDir := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = oldProjectDir })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("vault", false, "")
	cmd.Flags().Bool("prompts", false, "")
	cmd.Flags().String("model", "gemini-2.5-flash", "")
	cmd.Flags().String("skill", "", "")
	cmd.Flags().String("pack", "", "")
	cmd.Flags().Set("skill", "invalid-target")

	err := runInit(cmd, []string{})
	if err == nil {
		t.Fatal("expected error for invalid skill target")
	}
	if !strings.Contains(err.Error(), "unknown agent target") {
		t.Errorf("error should mention 'unknown agent target', got: %s", err.Error())
	}
}

func TestRunInit_PackOverride(t *testing.T) {
	dir := t.TempDir()
	oldProjectDir := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = oldProjectDir })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("vault", false, "")
	cmd.Flags().Bool("prompts", false, "")
	cmd.Flags().String("model", "gemini-2.5-flash", "")
	cmd.Flags().String("skill", "", "")
	cmd.Flags().String("pack", "", "")
	cmd.Flags().Set("skill", "claude-code")
	cmd.Flags().Set("pack", "meeting-notes")

	err := runInit(cmd, []string{})
	if err != nil {
		t.Fatalf("runInit with --pack override: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	s := strings.ToLower(string(content))
	if !strings.Contains(s, "meeting") {
		t.Error("--pack meeting-notes should produce meeting-related content")
	}
}

func TestRunInit_SkillOnExistingProject(t *testing.T) {
	dir := t.TempDir()
	oldProjectDir := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = oldProjectDir })

	// First, create a project with a custom config
	customConfig := "project: custom-project\nsources:\n  - path: src\n    type: code\noutput: _wiki\n"
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(customConfig), 0644)

	cmd := &cobra.Command{}
	cmd.Flags().Bool("vault", false, "")
	cmd.Flags().Bool("prompts", false, "")
	cmd.Flags().String("model", "gemini-2.5-flash", "")
	cmd.Flags().String("skill", "", "")
	cmd.Flags().String("pack", "", "")
	cmd.Flags().Set("skill", "claude-code")

	err := runInit(cmd, []string{})
	if err != nil {
		t.Fatalf("init --skill on existing project: %v", err)
	}

	// config.yaml should NOT be overwritten
	cfgContent, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !strings.Contains(string(cfgContent), "custom-project") {
		t.Error("config.yaml should be preserved (not overwritten)")
	}

	// CLAUDE.md should be generated with the project name from existing config
	claudeContent, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(claudeContent), "custom-project") {
		t.Error("CLAUDE.md should use project name from existing config")
	}
}
