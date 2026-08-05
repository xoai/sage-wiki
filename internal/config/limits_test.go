package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
)

func TestConfig_LimitsDefaults(t *testing.T) {
	cfg := Defaults()
	got := cfg.Limits.Resolve()
	want := limits.Limits{}.Resolve()
	if got != want {
		t.Fatalf("Defaults().Limits.Resolve() = %+v, want %+v", got, want)
	}
}

func TestConfig_LimitsLoad(t *testing.T) {
	write := func(t *testing.T, yaml string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := `
project: t
sources: [{path: raw}]
models: {write: m, query: m, embed: m}
`
	t.Run("partial limits load over defaults", func(t *testing.T) {
		p := write(t, base+"limits: {max_doc_bytes: 1024, provider_timeout: 5s}\n")
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		got := cfg.Limits.Resolve()
		if got.MaxDocBytes != 1024 {
			t.Errorf("MaxDocBytes = %d, want 1024", got.MaxDocBytes)
		}
		if got.ProviderTimeout != 5*time.Second {
			t.Errorf("ProviderTimeout = %v, want 5s", got.ProviderTimeout)
		}
		if got.MaxQueryBytes != limits.DefaultMaxQueryBytes {
			t.Errorf("MaxQueryBytes = %d, want default %d", got.MaxQueryBytes, limits.DefaultMaxQueryBytes)
		}
	})
	t.Run("no limits block gives all defaults", func(t *testing.T) {
		p := write(t, base)
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got, want := cfg.Limits.Resolve(), (limits.Limits{}.Resolve()); got != want {
			t.Errorf("Limits.Resolve() = %+v, want %+v", got, want)
		}
	})
	t.Run("negative limit fails validation", func(t *testing.T) {
		p := write(t, base+"limits: {max_doc_bytes: -1}\n")
		if _, err := Load(p); err == nil {
			t.Error("negative limits.max_doc_bytes must fail validation")
		}
	})
	t.Run("negative timeout fails validation", func(t *testing.T) {
		p := write(t, base+"limits: {provider_timeout: -5s}\n")
		if _, err := Load(p); err == nil {
			t.Error("negative limits.provider_timeout must fail validation")
		}
	})
}
