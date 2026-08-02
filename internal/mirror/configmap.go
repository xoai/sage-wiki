package mirror

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
)

// ConfigFromYAML maps the YAML-facing internal/config.MirrorConfig to the
// mirror's runtime Config (defaults resolved). Shared by the cmd layer and
// the serve hook (spec.md §APIs).
func ConfigFromYAML(wsDir string, mc config.MirrorConfig) (Config, error) {
	cfg := Config{
		Endpoint:             mc.Endpoint,
		Addressing:           mc.Addressing,
		Bucket:               mc.Bucket,
		Prefix:               mc.Prefix,
		Region:               mc.RegionOrDefault(),
		AccessKeyEnv:         mc.AccessKeyEnvOrDefault(),
		SecretKeyEnv:         mc.SecretKeyEnvOrDefault(),
		SessionTokenEnv:      mc.SessionTokenEnvOrDefault(),
		CredentialsFile:      mc.CredentialsFile,
		ShipInterval:         mc.ShipIntervalDur(),
		SnapshotInterval:     mc.SnapshotIntervalDur(),
		MinRotationInterval:  mc.MinRotationIntervalDur(),
		ShipLockTimeout:      mc.ShipLockTimeoutDur(),
		DrainTimeout:         mc.DrainTimeoutDur(),
		RetainGenerations:    mc.RetainGenerationsOrDefault(),
		MaxConsecutiveDefers: mc.MaxConsecutiveDefersOrDefault(),
		Encryption: EncryptionConfig{
			Enabled: mc.Encryption.Enabled,
			KeyFile: mc.Encryption.KeyFile,
		},
	}
	if cfg.Prefix == "" {
		cfg.Prefix = filepath.Base(wsDir)
	}
	if mc.Encryption.Enabled {
		if err := checkKeyFileOutside(wsDir, mc.Encryption.KeyFile); err != nil {
			return Config{}, err
		}
	}
	return cfg, nil
}

// checkKeyFileOutside enforces the spec's rule that the encryption key file
// lives OUTSIDE the workspace (it must never ship).
func checkKeyFileOutside(wsDir, keyFile string) error {
	if keyFile == "" {
		return fmt.Errorf("mirror: encryption.key_file required when encryption enabled")
	}
	absKey, err := filepath.Abs(keyFile)
	if err != nil {
		return fmt.Errorf("mirror: resolve key_file path: %w", err)
	}
	absWS, err := filepath.Abs(wsDir)
	if err != nil {
		return fmt.Errorf("mirror: resolve workspace path: %w", err)
	}
	if absKey == absWS || strings.HasPrefix(absKey, absWS+string(filepath.Separator)) {
		return fmt.Errorf("mirror: encryption.key_file must live OUTSIDE the workspace (it would ship otherwise)")
	}
	return nil
}
