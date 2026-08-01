package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/xoai/sage-wiki/internal/mirror"
)

var (
	hydrateEndpoint        string
	hydrateRegion          string
	hydrateCredentialsFile string
	hydrateGeneration      int
	hydrateAt              string
	hydratePartial         bool
	hydrateKeyFile         string
)

var hydrateCmd = &cobra.Command{
	Use:   "hydrate s3://<bucket>/<prefix> <DIR>",
	Short: "Restore a workspace from a remote mirror into an empty dir",
	Args:  cobra.ExactArgs(2),
	RunE:  runHydrate,
}

func init() {
	hydrateCmd.Flags().StringVar(&hydrateEndpoint, "endpoint", "", "S3-compatible endpoint URL (derived from --region for AWS when omitted)")
	hydrateCmd.Flags().StringVar(&hydrateRegion, "region", "auto", "SigV4 region")
	hydrateCmd.Flags().StringVar(&hydrateCredentialsFile, "credentials-file", "", "JSON credentials file (default: AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY env vars)")
	hydrateCmd.Flags().IntVar(&hydrateGeneration, "generation", 0, "Restore a specific generation (default: newest)")
	hydrateCmd.Flags().StringVar(&hydrateAt, "at", "", "Point-in-time restore (RFC3339; segment granularity, overshoot printed)")
	hydrateCmd.Flags().BoolVar(&hydratePartial, "partial", false, "Ordered restore with progress markers (lexical/graph available before vectors)")
	hydrateCmd.Flags().StringVar(&hydrateKeyFile, "key-file", "", "Encryption key file (required for encrypted mirrors)")
	rootCmd.AddCommand(hydrateCmd)
}

// parseS3URL splits s3://bucket/prefix into parts.
func parseS3URL(raw string) (bucket, prefix string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "s3" || u.Host == "" {
		return "", "", fmt.Errorf("hydrate: invalid s3:// URL %q (want s3://<bucket>/<prefix>)", raw)
	}
	return u.Host, strings.Trim(u.Path, "/"), nil
}

func runHydrate(cmd *cobra.Command, args []string) error {
	bucket, prefix, err := parseS3URL(args[0])
	if err != nil {
		return err
	}
	dst := args[1]

	endpoint := hydrateEndpoint
	if endpoint == "" {
		if hydrateRegion == "" || hydrateRegion == "auto" {
			return fmt.Errorf("hydrate: --endpoint is required for region=auto (R2/MinIO); for AWS pass --region and it is derived")
		}
		endpoint = "https://s3." + hydrateRegion + ".amazonaws.com"
	}

	opts := mirror.HydrateOpts{
		Generation: hydrateGeneration,
		Partial:    hydratePartial,
		KeyFile:    hydrateKeyFile,
	}
	if hydrateAt != "" {
		at, err := time.Parse(time.RFC3339, hydrateAt)
		if err != nil {
			return fmt.Errorf("hydrate: --at must be RFC3339: %w", err)
		}
		opts.At = at
	}

	cfg := mirror.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Prefix:          prefix,
		Region:          hydrateRegion,
		AccessKeyEnv:    "AWS_ACCESS_KEY_ID",
		SecretKeyEnv:    "AWS_SECRET_ACCESS_KEY",
		CredentialsFile: hydrateCredentialsFile,
	}

	rep, err := mirror.Hydrate(orBackground(cmd), cfg, dst, opts)
	if err != nil {
		return err
	}
	for _, a := range rep.Advisories {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n", a)
	}
	if rep.Overshoot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "PITR note: %s\n", rep.Overshoot)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "hydrated generation %d into %s\n", rep.Generation, dst)
	fmt.Fprintf(cmd.OutOrStdout(), "note: config.yaml is NOT mirrored (it can hold secrets) — run `sage-wiki init` or restore your own config before compiling\n")
	return nil
}
