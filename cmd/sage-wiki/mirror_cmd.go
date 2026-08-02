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

var mirrorVerifyFast bool

var mirrorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show local + remote mirror state",
	RunE:  runMirrorStatus,
}

var mirrorVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Check the remote invariant (full re-hash by default; --fast is HEAD-only)",
	RunE:  runMirrorVerify,
}

var mirrorSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Force a new generation (checkpoint + snapshot + commit)",
	RunE:  runMirrorSnapshot,
}

func init() {
	mirrorVerifyCmd.Flags().BoolVar(&mirrorVerifyFast, "fast", false, "HEAD-only existence check (skip full re-hash)")
	mirrorCmd.AddCommand(mirrorEnableCmd, mirrorStatusCmd, mirrorVerifyCmd, mirrorSnapshotCmd)
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
	if _, statErr := os.Stat(filepath.Join(dir, ".sage", "wiki.db")); statErr == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "mirror enabled: s3://%s/%s (generation 1 bootstrapped)\n", cfg.Mirror.Bucket, mirror.NormalizePrefix(mcfg.Prefix))
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mirror enabled: s3://%s/%s (manifest written; generation 1 bootstraps on the first pass with a database)\n", cfg.Mirror.Bucket, mirror.NormalizePrefix(mcfg.Prefix))
	return nil
}

func runMirrorStatus(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	cfg, err := config.Load(resolveConfigPath(dir))
	if err != nil {
		return err
	}
	// F-081: disabled workspaces report enabled:false WITHOUT demanding
	// credentials (Open resolves creds unconditionally).
	if !cfg.Mirror.Enabled {
		if outputFormat == "json" {
			fmt.Fprintln(cmd.OutOrStdout(), cli.FormatJSON(true, mirror.Status{Enabled: false}, ""))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "mirror: disabled (mirror.enabled is not set)\n")
		}
		return nil
	}
	mcfg, err := mirror.ConfigFromYAML(dir, cfg.Mirror)
	if err != nil {
		return err
	}
	// F-080: status must run the real diff — pending_changes/lag depend on
	// it; F-097: read-only variant (status must not write the cache).
	m, err := mirror.Open(dir, mcfg, mirror.NewDiffChangeSourceReadOnly(dir))
	if err != nil {
		return err
	}
	s, err := m.Status(orBackground(cmd))
	if err != nil {
		return err
	}
	if outputFormat == "json" {
		fmt.Fprintln(cmd.OutOrStdout(), cli.FormatJSON(true, s, ""))
		return nil
	}
	if !s.Enabled {
		fmt.Fprintf(cmd.OutOrStdout(), "mirror: not enabled remotely (no mirror-state.json)\n")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mirror: generation %d, last commit %s\n", s.RemoteGeneration, s.LastCommit.Format("2006-01-02 15:04:05 UTC"))
	fmt.Fprintf(cmd.OutOrStdout(), "pending: %d change(s), lag %ds\n", s.PendingChanges, s.LagSeconds)
	if s.PendingRotation {
		fmt.Fprintf(cmd.OutOrStdout(), "pending rotation (fold debounce) — ships next pass\n")
	}
	if s.RotationDeferred > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: rotation deferred %d times (busy writer) — WAL list grows unbounded\n", s.RotationDeferred)
	}
	if s.ServeRestartNote {
		fmt.Fprintf(cmd.OutOrStdout(), "note: a serve holds this workspace — mirror takes effect in-process at serve restart\n")
	}
	return nil
}

func runMirrorVerify(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	_, mcfg, err := mirrorConfigFor(dir)
	if err != nil {
		return err
	}
	m, err := mirror.Open(dir, mcfg, nil)
	if err != nil {
		return err
	}
	var rep mirror.Report
	if mirrorVerifyFast {
		rep, err = m.VerifyFast(orBackground(cmd))
	} else {
		rep, err = m.Verify(orBackground(cmd))
	}
	if err != nil {
		return err
	}
	if outputFormat == "json" {
		fmt.Fprintln(cmd.OutOrStdout(), cli.FormatJSON(true, rep, ""))
	} else {
		for _, v := range rep.Violations {
			fmt.Fprintf(cmd.OutOrStdout(), "VIOLATION: %s\n", v)
		}
		for _, a := range rep.Advisories {
			fmt.Fprintf(cmd.OutOrStdout(), "advisory: %s\n", a)
		}
		if rep.Valid {
			fmt.Fprintf(cmd.OutOrStdout(), "mirror verify: VALID (%d objects checked, generation %d)\n", rep.Checked, rep.Generation)
		}
	}
	if !rep.Valid {
		return fmt.Errorf("mirror verify: %d violation(s)", len(rep.Violations))
	}
	return nil
}

func runMirrorSnapshot(cmd *cobra.Command, args []string) error {
	dir, _ := filepath.Abs(projectDir)
	_, mcfg, err := mirrorConfigFor(dir)
	if err != nil {
		return err
	}
	m, err := mirror.Open(dir, mcfg, nil)
	if err != nil {
		return err
	}
	id, err := m.Snapshot(orBackground(cmd))
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mirror snapshot committed: %s\n", id)
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
