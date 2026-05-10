package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/skill"
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Generate or refresh agent skill files",
}

var skillRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Generate or refresh the agent skill file for this project",
	RunE:  runSkillRefresh,
}

var skillPreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Preview the agent skill file without writing",
	RunE:  runSkillPreview,
}

func init() {
	skillRefreshCmd.Flags().String("target", "claude-code", "Agent target (claude-code, cursor, windsurf, agents-md, codex, gemini, generic)")
	skillPreviewCmd.Flags().String("target", "claude-code", "Agent target")

	skillCmd.AddCommand(skillRefreshCmd, skillPreviewCmd)
}

func loadSkillConfig(cmd *cobra.Command) (*config.Config, skill.AgentTarget, error) {
	dir, _ := filepath.Abs(projectDir)
	cfgPath := resolveConfigPath(dir)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, "", fmt.Errorf("no config.yaml found; run sage-wiki init first")
	}

	targetStr, _ := cmd.Flags().GetString("target")
	target := skill.AgentTarget(targetStr)
	if _, err := skill.TargetInfoFor(target); err != nil {
		return nil, "", err
	}

	return cfg, target, nil
}

func runSkillRefresh(cmd *cobra.Command, args []string) error {
	cfg, target, err := loadSkillConfig(cmd)
	if err != nil {
		return err
	}

	dir, _ := filepath.Abs(projectDir)
	data := skill.BuildTemplateData(cfg)

	if err := skill.WriteSkill(dir, target, data); err != nil {
		return err
	}

	info, _ := skill.TargetInfoFor(target)
	fmt.Printf("Agent skill written to %s\n", info.FileName)
	return nil
}

func runSkillPreview(cmd *cobra.Command, args []string) error {
	cfg, target, err := loadSkillConfig(cmd)
	if err != nil {
		return err
	}

	data := skill.BuildTemplateData(cfg)
	out, err := skill.PreviewSkill(target, data)
	if err != nil {
		return err
	}

	fmt.Println(out)
	return nil
}
