package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_VectorsDefaults(t *testing.T) {
	cfg := Defaults()
	if got := cfg.VectorBackend(); got != "memory" {
		t.Errorf("VectorBackend default = %q, want memory", got)
	}
	if got := cfg.VectorQuantization(); got != "none" {
		t.Errorf("VectorQuantization default = %q, want none", got)
	}
}

func TestConfig_VectorsValidate(t *testing.T) {
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
	t.Run("valid mmap+int8", func(t *testing.T) {
		p := write(t, base+"vectors: {backend: mmap, quantization: int8}\n")
		cfg, err := Load(p)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.VectorBackend() != "mmap" || cfg.VectorQuantization() != "int8" {
			t.Errorf("got %s/%s", cfg.VectorBackend(), cfg.VectorQuantization())
		}
	})
	t.Run("bad backend", func(t *testing.T) {
		p := write(t, base+"vectors: {backend: zstd}\n")
		if _, err := Load(p); err == nil {
			t.Error("invalid vectors.backend must fail validation")
		}
	})
	t.Run("bad quantization", func(t *testing.T) {
		p := write(t, base+"vectors: {quantization: fp16}\n")
		if _, err := Load(p); err == nil {
			t.Error("invalid vectors.quantization must fail validation")
		}
	})
}
