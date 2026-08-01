package mirror

import (
	"context"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// Config is the mirror's runtime configuration (resolved from
// internal/config.MirrorConfig by the cmd layer — see spec.md §APIs).
type Config struct {
	Endpoint, Bucket, Prefix, Region string
	AccessKeyEnv, SecretKeyEnv       string
	CredentialsFile                  string
	ShipInterval                     durationField
	SnapshotInterval                 durationField
	MinRotationInterval              durationField
	ShipLockTimeout                  durationField
	DrainTimeout                     durationField
	RetainGenerations                int
	MaxConsecutiveDefers             int
	Encryption                       EncryptionConfig
}

// EncryptionConfig tunes optional client-side encryption (off by default).
type EncryptionConfig struct {
	Enabled bool
	KeyFile string
}

// ops is the internal surface the facade delegates to; Tasks 8/9/12/19 wire
// the real implementations behind it (plan.md Task 7A).
type ops interface {
	Enable(ctx context.Context) error
	Ship(ctx context.Context, batch pkmirror.ChangeBatch) error
	Snapshot(ctx context.Context) (pkmirror.SnapshotID, error)
	Status(ctx context.Context) (Status, error)
	Verify(ctx context.Context) (Report, error)
	Hydrate(ctx context.Context, dst string) error
}

// Mirror is the concrete remote mirror (spec.md §Components row 9): it
// implements the pkg/mirror.Mirror seam and adds Enable/Status/Verify for
// the CLI. Lock-free: a read-only SQLite participant.
type Mirror struct {
	dir    string
	cfg    Config
	src    ChangeSource
	client *s3.Client
	local  *LocalState
	ops    ops
}

// Open builds a Mirror: resolves credentials, constructs the S3 client,
// loads local ship-state. Never takes engine.lock.
func Open(wsDir string, cfg Config, src ChangeSource) (*Mirror, error) {
	creds, err := ResolveCredentials(cfg.AccessKeyEnv, cfg.SecretKeyEnv, cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	client, err := s3.NewClient(cfg.Endpoint, cfg.Region, creds, s3.WithPathStyle(true))
	if err != nil {
		return nil, err
	}
	local, err := LoadLocalState(localStatePath(wsDir))
	if err != nil {
		return nil, err
	}
	m := &Mirror{dir: wsDir, cfg: cfg, src: src, client: client, local: local}
	// Real ops are wired by later tasks; the facade is nil-guarded until
	// then (Open alone cannot ship/verify without the M2-M4 internals).
	return m, nil
}

func localStatePath(dir string) string {
	return dir + "/.sage/mirror-local.json"
}

func (m *Mirror) notReady(what string) error {
	return &NotReadyError{Op: what}
}

// Enable implements ops.
func (m *Mirror) Enable(ctx context.Context) error {
	if m.ops == nil {
		return m.notReady("enable")
	}
	return m.ops.Enable(ctx)
}

// Ship implements the pkg/mirror.Mirror seam.
func (m *Mirror) Ship(ctx context.Context, batch pkmirror.ChangeBatch) error {
	if m.ops == nil {
		return m.notReady("ship")
	}
	return m.ops.Ship(ctx, batch)
}

// Snapshot implements the pkg/mirror.Mirror seam.
func (m *Mirror) Snapshot(ctx context.Context) (pkmirror.SnapshotID, error) {
	if m.ops == nil {
		return "", m.notReady("snapshot")
	}
	return m.ops.Snapshot(ctx)
}

// Status reports local + remote mirror state.
func (m *Mirror) Status(ctx context.Context) (Status, error) {
	if m.ops == nil {
		return Status{}, m.notReady("status")
	}
	return m.ops.Status(ctx)
}

// Verify checks the remote invariant.
func (m *Mirror) Verify(ctx context.Context) (Report, error) {
	if m.ops == nil {
		return Report{}, m.notReady("verify")
	}
	return m.ops.Verify(ctx)
}

// Hydrate implements the pkg/mirror.Mirror seam (newest generation, full).
func (m *Mirror) Hydrate(ctx context.Context, dst string) error {
	if m.ops == nil {
		return m.notReady("hydrate")
	}
	return m.ops.Hydrate(ctx, dst)
}
