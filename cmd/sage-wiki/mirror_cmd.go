package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/xoai/sage-wiki/internal/cli"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/mirror"
)

var mirrorCmd = &cobra.Command{
	Use:   "mirror",
	Short: "Remote mirror (S3-compatible backup, WAL shipping, hydrate)",
}

var mirrorEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Validate config+credentials, write the bucket manifest, bootstrap generation 1",
	RunE:  runMirrorEnable,
}

func init() {
	mirrorCmd.AddCommand(mirrorEnableCmd)
}

// mirrorConfigFor loads the workspace config and resolves the mirror
// runtime config. Shared by all mirror subcommands.
func mirrorConfigFor(dir string) (*config.Config, mirror.Config, error) {
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return nil, mirror.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, mirror.Config{}, err
	}
	mcfg, err := mirror.ConfigFromYAML(dir, cfg.Mirror)
	if err != nil {
		return nil, mirror.Config{}, err
	}
	return cfg, mcfg, nil
}

func runMirrorEnable(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	cfg, mcfg, err := mirrorConfigFor(dir)
	if err != nil {
		return err
	}
	if cfg.Mirror.Endpoint == "" || cfg.Mirror.Bucket == "" {
		return fmt.Errorf("mirror: configure the mirror: block (endpoint, bucket) in config.yaml first")
	}

	m, err := mirror.Open(dir, mcfg, nil)
	if err != nil {
		return err
	}
	if err := m.Enable(orBackground(cmd)); err != nil {
		if errors.Is(err, mirror.ErrAlreadyEnabled) {
			fmt.Fprintf(cmd.OutOrStdout(), "mirror already enabled for this workspace\n")
			return ensureMirrorEnabledFlag(dir, cfg)
		}
		return err
	}

	if err := ensureMirrorEnabledFlag(dir, cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mirror enabled: s3://%s/%s (generation 1 bootstrapped)\n", cfg.Mirror.Bucket, mirror.NormalizePrefix(mcfg.Prefix))
	return nil
}

// ensureMirrorEnabledFlag persists mirror.enabled: true to config.yaml.
func ensureMirrorEnabledFlag(dir string, cfg *config.Config) error {
	if cfg.Mirror.Enabled {
		return nil
	}
	cfg.Mirror.Enabled = true
	if err := cfg.Save(resolveConfigPath(dir)); err != nil {
		return fmt.Errorf("mirror: write config: %w", err)
	}
	return nil
}

// orBackground normalizes cmd.Context(), which is nil outside Execute
// (direct RunE calls in tests).
func orBackground(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

var _ = cli.FormatJSON // used by status (Task 9)
var _ = os.Getenv
