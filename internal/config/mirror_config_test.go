package config

import (
	"testing"
	"time"
)

func mirrorBase() *Config {
	return &Config{
		Project: "p",
		Output:  "wiki",
		Sources: []Source{{Path: "raw"}},
		Mirror: MirrorConfig{
			Enabled:  true,
			Endpoint: "http://localhost:9000",
			Bucket:   "backups",
		},
	}
}

func TestMirrorConfig_Validates(t *testing.T) {
	if err := mirrorBase().Validate(); err != nil {
		t.Fatalf("valid mirror config rejected: %v", err)
	}
}

func TestMirrorConfig_DisabledSkipsEndpointBucket(t *testing.T) {
	c := mirrorBase()
	c.Mirror.Enabled = false
	c.Mirror.Endpoint = ""
	c.Mirror.Bucket = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled mirror should not require endpoint/bucket: %v", err)
	}
}

func TestMirrorConfig_RequiresEndpointAndBucket(t *testing.T) {
	c := mirrorBase()
	c.Mirror.Endpoint = ""
	if err := c.Validate(); err == nil {
		t.Fatal("enabled mirror without endpoint should fail")
	}
	c = mirrorBase()
	c.Mirror.Bucket = ""
	if err := c.Validate(); err == nil {
		t.Fatal("enabled mirror without bucket should fail")
	}
}

// TestMirrorConfig_RejectsInlineSecrets: credentials NEVER live in config
// values (spec.md §Config — forward-compat guard).
func TestMirrorConfig_RejectsInlineSecrets(t *testing.T) {
	for _, mutate := range []func(*MirrorConfig){
		func(m *MirrorConfig) { m.AccessKey = "AKIDEXAMPLE" },
		func(m *MirrorConfig) { m.SecretKey = "secret" },
	} {
		c := mirrorBase()
		mutate(&c.Mirror)
		if err := c.Validate(); err == nil {
			t.Fatal("inline secret key should be rejected")
		}
	}
	// Rejected even when disabled (the guard is unconditional).
	c := mirrorBase()
	c.Mirror.Enabled = false
	c.Mirror.AccessKey = "AKIDEXAMPLE"
	if err := c.Validate(); err == nil {
		t.Fatal("inline secret should be rejected even when mirror disabled")
	}
}

func TestMirrorConfig_DurationsParse(t *testing.T) {
	c := mirrorBase()
	c.Mirror.ShipInterval = "not-a-duration"
	if err := c.Validate(); err == nil {
		t.Fatal("bad ship_interval should fail")
	}
	c = mirrorBase()
	c.Mirror.ShipInterval = "500ms"
	c.Mirror.SnapshotInterval = "30m"
	c.Mirror.MinRotationInterval = "45s"
	c.Mirror.ShipLockTimeout = "2s"
	c.Mirror.DrainTimeout = "5s"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid durations rejected: %v", err)
	}
}

func TestMirrorConfig_RetainGenerationsFloor(t *testing.T) {
	c := mirrorBase()
	c.Mirror.RetainGenerations = -1
	if err := c.Validate(); err == nil {
		t.Fatal("negative retain_generations should fail")
	}
}

func TestMirrorConfig_EncryptionRequiresKeyFile(t *testing.T) {
	c := mirrorBase()
	c.Mirror.Encryption.Enabled = true
	if err := c.Validate(); err == nil {
		t.Fatal("encryption without key_file should fail")
	}
	c.Mirror.Encryption.KeyFile = "/etc/sage-wiki/mirror.key"
	if err := c.Validate(); err != nil {
		t.Fatalf("encryption with key_file rejected: %v", err)
	}
}

func TestMirrorConfig_Defaults(t *testing.T) {
	m := &MirrorConfig{}
	defaults := map[string]time.Duration{
		"ShipInterval":        m.ShipIntervalDur(),
		"SnapshotInterval":    m.SnapshotIntervalDur(),
		"MinRotationInterval": m.MinRotationIntervalDur(),
		"ShipLockTimeout":     m.ShipLockTimeoutDur(),
		"DrainTimeout":        m.DrainTimeoutDur(),
	}
	want := map[string]time.Duration{
		"ShipInterval":        time.Second,
		"SnapshotInterval":    time.Hour,
		"MinRotationInterval": 60 * time.Second,
		"ShipLockTimeout":     5 * time.Second,
		"DrainTimeout":        10 * time.Second,
	}
	for name, got := range defaults {
		if got != want[name] {
			t.Fatalf("%s default = %v, want %v", name, got, want[name])
		}
	}
	if m.RegionOrDefault() != "auto" {
		t.Fatalf("region default = %q", m.RegionOrDefault())
	}
	if m.AccessKeyEnvOrDefault() != "AWS_ACCESS_KEY_ID" {
		t.Fatalf("access_key_env default = %q", m.AccessKeyEnvOrDefault())
	}
	if m.SecretKeyEnvOrDefault() != "AWS_SECRET_ACCESS_KEY" {
		t.Fatalf("secret_key_env default = %q", m.SecretKeyEnvOrDefault())
	}
	if m.RetainGenerationsOrDefault() != 2 {
		t.Fatalf("retain default = %d", m.RetainGenerationsOrDefault())
	}
	if m.MaxConsecutiveDefersOrDefault() != 10 {
		t.Fatalf("defers default = %d", m.MaxConsecutiveDefersOrDefault())
	}
}

func TestMirrorConfig_AddressingValidation(t *testing.T) {
	c := mirrorBase()
	c.Mirror.Addressing = "sideways"
	if err := c.Validate(); err == nil {
		t.Fatal("invalid addressing should fail")
	}
	c = mirrorBase()
	c.Mirror.Addressing = "virtual"
	if err := c.Validate(); err != nil {
		t.Fatalf("virtual addressing rejected: %v", err)
	}
}
