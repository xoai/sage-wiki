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
	skillRefreshCmd.Flags().String("pack", "", "Override domain pack")

	skillPreviewCmd.Flags().String("target", "claude-code", "Agent target")
	skillPreviewCmd.Flags().String("pack", "", "Override domain pack")

	skillCmd.AddCommand(skillRefreshCmd, skillPreviewCmd)
}

func loadSkillConfig(cmd *cobra.Command) (*config.Config, skill.AgentTarget, skill.PackName, error) {
	dir, _ := filepath.Abs(projectDir)
	cfgPath := resolveConfigPath(dir)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("no config.yaml found; run sage-wiki init first")
	}

	targetStr, _ := cmd.Flags().GetString("target")
	target := skill.AgentTarget(targetStr)
	if _, err := skill.TargetInfoFor(target); err != nil {
		return nil, "", "", err
	}

	packStr, _ := cmd.Flags().GetString("pack")
	var pack skill.PackName
	if packStr != "" {
		pack = skill.PackName(packStr)
	} else {
		pack = skill.SelectPack(cfg.Sources)
	}

	return cfg, target, pack, nil
}

func runSkillRefresh(cmd *cobra.Command, args []string) error {
	cfg, target, pack, err := loadSkillConfig(cmd)
	if err != nil {
		return err
	}

	dir, _ := filepath.Abs(projectDir)
	data := skill.BuildTemplateData(cfg)

	if err := skill.WriteSkill(dir, target, pack, data); err != nil {
		return err
	}

	info, _ := skill.TargetInfoFor(target)
	fmt.Printf("Agent skill written to %s\n", info.FileName)
	return nil
}

func runSkillPreview(cmd *cobra.Command, args []string) error {
	cfg, target, pack, err := loadSkillConfig(cmd)
	if err != nil {
		return err
	}

	data := skill.BuildTemplateData(cfg)
	out, err := skill.PreviewSkill(target, pack, data)
	if err != nil {
		return err
	}

	fmt.Println(out)
	return nil
}
