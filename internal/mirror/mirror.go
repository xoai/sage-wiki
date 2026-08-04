package mirror

import (
	"context"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
	"github.com/xoai/sage-wiki/pkg/events"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// Config is the mirror's runtime configuration (resolved from
// internal/config.MirrorConfig by the cmd layer — see spec.md §APIs).
type Config struct {
	Endpoint, Bucket, Prefix, Region string
	Addressing                       string // "auto" (default) | "path" | "virtual"
	AccessKeyEnv, SecretKeyEnv       string
	SessionTokenEnv                  string
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
	VerifyMode(ctx context.Context, fast bool) (Report, error)
	Hydrate(ctx context.Context, dst string) error
}

// Mirror is the concrete remote mirror (spec.md §Components row 9): it
// implements the pkg/mirror.Mirror seam and adds Enable/Status/Verify for
// the CLI. Lock-free: a read-only SQLite participant.
type Mirror struct {
	dir         string
	cfg         Config
	src         ChangeSource
	client      *s3.Client
	local       *LocalState
	ops         ops
	now         func() time.Time // injected clock (status lag; tests)
	pruneDelete func(ctx context.Context, bucket, key string) error

	encKey []byte // AES-256 key when encryption.enabled (nil otherwise)

	lastPruneWarnings []string

	// sink receives the mirror events (SPEC-07): mirror_shipped per ship
	// pass, mirror_snapshot per rotation. nil = no events.
	sink events.Sink

	// lastSnapshotBytes records the byte size of the most recent rotation
	// snapshot so emitSnapshot reports real data, never a fabricated zero.
	lastSnapshotBytes atomic.Int64
}

// SetEventSink installs the mirror event sink (SPEC-07 narrow setter).
func (m *Mirror) SetEventSink(sink events.Sink) {
	m.sink = events.NilSafe(sink) // typed-nil guard — see events.NilSafe
}

// normalize applies spec defaults to a zero-value-constructed Config so a
// Mirror built without internal/config still behaves per spec (direct
// construction happens in tests and the hydrate path).
func (c *Config) normalize() {
	if c.Region == "" {
		c.Region = "auto"
	}
	if c.AccessKeyEnv == "" {
		c.AccessKeyEnv = "AWS_ACCESS_KEY_ID"
	}
	if c.SecretKeyEnv == "" {
		c.SecretKeyEnv = "AWS_SECRET_ACCESS_KEY"
	}
	if c.SessionTokenEnv == "" {
		c.SessionTokenEnv = "AWS_SESSION_TOKEN"
	}
	if c.RetainGenerations == 0 {
		c.RetainGenerations = 2
	}
	if c.MaxConsecutiveDefers == 0 {
		c.MaxConsecutiveDefers = 10
	}
	if c.ShipLockTimeout == 0 {
		c.ShipLockTimeout = 5 * time.Second
	}
	if c.ShipInterval == 0 {
		c.ShipInterval = time.Second
	}
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = time.Hour
	}
	if c.MinRotationInterval == 0 {
		c.MinRotationInterval = 60 * time.Second
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = 10 * time.Second
	}
	if c.Addressing == "" {
		c.Addressing = "auto"
	}
}

// pathStyle resolves the addressing mode (F-115): "path" forces path-style,
// "virtual" forces virtual-host, "auto" selects virtual-host for AWS
// endpoints (path-style is deprecated there) and path-style otherwise
// (MinIO/R2 require path-style by default).
func (c *Config) pathStyle() bool {
	switch c.Addressing {
	case "path":
		return true
	case "virtual":
		return false
	default: // auto
		u, err := url.Parse(c.Endpoint)
		if err != nil {
			return true // unparseable → path-style (client validates endpoint anyway)
		}
		host := u.Hostname()
		return host != "amazonaws.com" && !strings.HasSuffix(host, ".amazonaws.com")
	}
}

// Open builds a Mirror: resolves credentials, constructs the S3 client,
// loads local ship-state. Never takes engine.lock.
func Open(wsDir string, cfg Config, src ChangeSource) (*Mirror, error) {
	cfg.normalize()
	creds, err := ResolveCredentials(cfg.AccessKeyEnv, cfg.SecretKeyEnv, cfg.SessionTokenEnv, cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	client, err := s3.NewClient(cfg.Endpoint, cfg.Region, creds, s3.WithPathStyle(cfg.pathStyle()))
	if err != nil {
		return nil, err
	}
	local, err := LoadLocalState(localStatePath(wsDir))
	if err != nil {
		return nil, err
	}
	m := &Mirror{dir: wsDir, cfg: cfg, src: src, client: client, local: local, now: time.Now}
	m.pruneDelete = m.pruneDeleteDefault
	if err := m.loadEncryptionKey(); err != nil {
		return nil, err
	}
	// Wire the real ops (enable.go's init sets openWiresOps).
	if openWiresOps != nil {
		openWiresOps(m)
	}
	return m, nil
}

// openWiresOps is set by enable.go's init to attach the real ops behind the
// facade (kept a var so the facade file stays free of implementation deps).
var openWiresOps func(*Mirror)

// Config returns the mirror's runtime configuration (read-only).
func (m *Mirror) Config() Config { return m.cfg }

// ScheduledRotationDue reports whether the scheduled rotation cadence
// (snapshot_interval) has elapsed since the last generation commit of ANY
// kind — used by the serve shipper ticker. Reads the FILE (the shared
// truth): m.local is concurrently reassigned by Ship/Snapshot on the
// rotation goroutine (data race otherwise, caught by -race).
func (m *Mirror) ScheduledRotationDue() bool {
	local, err := LoadLocalState(localStatePath(m.dir))
	if err != nil {
		return false
	}
	return m.now().Sub(local.LastRotationAt) >= m.cfg.SnapshotInterval
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

// Verify checks the remote invariant (full re-hash).
func (m *Mirror) Verify(ctx context.Context) (Report, error) {
	if m.ops == nil {
		return Report{}, m.notReady("verify")
	}
	return m.ops.VerifyMode(ctx, false)
}

// VerifyFast checks existence only (HEAD pass over live + retained
// generations) — spec's --fast mode.
func (m *Mirror) VerifyFast(ctx context.Context) (Report, error) {
	if m.ops == nil {
		return Report{}, m.notReady("verify")
	}
	return m.ops.VerifyMode(ctx, true)
}

// Hydrate implements the pkg/mirror.Mirror seam (newest generation, full).
func (m *Mirror) Hydrate(ctx context.Context, dst string) error {
	if m.ops == nil {
		return m.notReady("hydrate")
	}
	return m.ops.Hydrate(ctx, dst)
}
