package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	full := "project: test\noutput: wiki\nsources:\n  - path: raw\n    type: dir\n" + body
	if err := os.WriteFile(path, []byte(full), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStorageConfigDefaults(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Backend != "sqlite" {
		t.Errorf("default backend = %q, want sqlite", cfg.Storage.Backend)
	}
	if cfg.Storage.LockTimeout != "5s" {
		t.Errorf("default lock_timeout = %q, want 5s", cfg.Storage.LockTimeout)
	}
	if cfg.Storage.Pool.MaxOpen != 10 || cfg.Storage.Pool.MaxIdle != 2 {
		t.Errorf("default pool = %d/%d, want 10/2", cfg.Storage.Pool.MaxOpen, cfg.Storage.Pool.MaxIdle)
	}
	d, err := cfg.Storage.LockTimeoutDuration()
	if err != nil {
		t.Fatalf("LockTimeoutDuration: %v", err)
	}
	if d.Seconds() != 5 {
		t.Errorf("lock timeout duration = %v, want 5s", d)
	}
}

func TestStorageConfigPostgresValid(t *testing.T) {
	t.Setenv("TEST_PG_DSN", "postgres://u:p@host:5432/db")
	cfg, err := Load(writeTempConfig(t, `
storage:
  backend: postgres
  dsn: ${TEST_PG_DSN}
  vector_dimension: 768
  lock_timeout: "10s"
  pool:
    max_open: 20
    max_idle: 4
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Storage.Backend != "postgres" {
		t.Errorf("backend = %q, want postgres", cfg.Storage.Backend)
	}
	if cfg.Storage.DSN != "postgres://u:p@host:5432/db" {
		t.Errorf("dsn not expanded: %q", cfg.Storage.DSN)
	}
	if cfg.Storage.VectorDimension != 768 {
		t.Errorf("vector_dimension = %d, want 768", cfg.Storage.VectorDimension)
	}
	d, err := cfg.Storage.LockTimeoutDuration()
	if err != nil || d.Seconds() != 10 {
		t.Errorf("lock timeout = %v, %v; want 10s", d, err)
	}
	if cfg.Storage.Pool.MaxOpen != 20 || cfg.Storage.Pool.MaxIdle != 4 {
		t.Errorf("pool = %d/%d, want 20/4", cfg.Storage.Pool.MaxOpen, cfg.Storage.Pool.MaxIdle)
	}
}

func TestStorageConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		storage string
		wantErr string
	}{
		{"postgres missing dsn", `
storage:
  backend: postgres
  vector_dimension: 768
`, "storage.dsn required"},
		{"postgres missing dimension", `
storage:
  backend: postgres
  dsn: postgres://x
`, "storage.vector_dimension required"},
		{"bad lock_timeout", `
storage:
  lock_timeout: "not-a-duration"
`, "storage.lock_timeout"},
		{"unknown backend", `
storage:
  backend: mysql
`, "invalid storage.backend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, tc.storage))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if got := err.Error(); !strings.Contains(got, tc.wantErr) {
				t.Fatalf("error %q does not contain %q", got, tc.wantErr)
			}
		})
	}
}
