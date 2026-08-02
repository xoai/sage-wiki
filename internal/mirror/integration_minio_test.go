package mirror

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// MinIO-backed integration tests (spec AC-5): env-gated SAGE_TEST_MINIO=1,
// endpoint SAGE_TEST_MINIO_ENDPOINT (default http://localhost:9000), creds
// minioadmin/minioadmin via env (CI sets them; never real cloud creds).
// Offline `go test ./...` SKIPS — zero network I/O by default.

func minioConfig(t *testing.T, bucket string) Config {
	t.Helper()
	if os.Getenv("SAGE_TEST_MINIO") != "1" {
		t.Skip("MinIO integration disabled (SAGE_TEST_MINIO=1 to run)")
	}
	endpoint := os.Getenv("SAGE_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:9000"
	}
	return Config{
		Endpoint:     endpoint,
		Bucket:       bucket,
		Prefix:       "it/",
		Region:       "auto",
		AccessKeyEnv: "MINIO_ROOT_USER",
		SecretKeyEnv: "MINIO_ROOT_PASSWORD",
	}
}

func setMinioCreds(t *testing.T) {
	t.Helper()
	if os.Getenv("MINIO_ROOT_USER") == "" {
		t.Setenv("MINIO_ROOT_USER", "minioadmin")
	}
	if os.Getenv("MINIO_ROOT_PASSWORD") == "" {
		t.Setenv("MINIO_ROOT_PASSWORD", "minioadmin")
	}
}

func minioWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return dir
}

// TestMinIO_RoundTrip: enable → mutate+ship → wipe → hydrate → content
// parity against a REAL S3 implementation (SigV4 client proof).
func TestMinIO_RoundTrip(t *testing.T) {
	setMinioCreds(t)
	cfg := minioConfig(t, "sage-it-roundtrip")
	dir := minioWorkspace(t)
	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (MinIO up?): %v", err)
	}
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo MinIO")
	db, _ := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	db.Exec("INSERT INTO t (v) VALUES ('minio-row')")
	db.Close()
	ageLocalRotationFile(t, dir, -2*time.Hour)
	if _, err := m.shipPass(context.Background()); err != nil {
		t.Fatalf("ship: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if !rep.Valid {
		t.Fatalf("report = %+v", rep)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "wiki/concepts/Foo.md"))
	if string(b) != "# Foo MinIO" {
		t.Fatalf("Foo.md = %q", b)
	}
	rdb, _ := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	defer rdb.Close()
	var n int
	rdb.QueryRow("SELECT COUNT(*) FROM t WHERE v='minio-row'").Scan(&n)
	if n != 1 {
		t.Fatal("db row lost in round-trip")
	}
	// Verify against the real bucket.
	vrep, err := m.Verify(context.Background())
	if err != nil || !vrep.Valid {
		t.Fatalf("verify: %+v %v", vrep, err)
	}
}

// TestMinIO_CorruptionDetects: flip one byte in exactly one stored object →
// full verify ALWAYS fails naming it; --fast passes (deterministic both).
func TestMinIO_CorruptionDetects(t *testing.T) {
	setMinioCreds(t)
	cfg := minioConfig(t, "sage-it-corrupt")
	dir := minioWorkspace(t)
	m, _ := Open(dir, cfg, NewDiffChangeSource(dir))
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	// Corrupt the snapshot object IN the real bucket: download, flip, re-PUT.
	st, err := m.remoteState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.client.GetObject(context.Background(), cfg.Bucket, st.DB.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xFF
	if err := m.client.PutObject(context.Background(), cfg.Bucket, st.DB.Snapshot, b); err != nil {
		t.Fatal(err)
	}
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("full verify must fail on single-byte corruption")
	}
	fast, err := m.VerifyFast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fast.Valid {
		t.Fatal("--fast (existence) must pass despite corruption")
	}
}

// TestMinIO_EncryptionRoundTrip against the real bucket.
func TestMinIO_EncryptionRoundTrip(t *testing.T) {
	setMinioCreds(t)
	keyPath := writeKeyFile(t)
	cfg := minioConfig(t, "sage-it-crypt")
	cfg.Encryption = EncryptionConfig{Enabled: true, KeyFile: keyPath}
	dir := minioWorkspace(t)
	m, _ := Open(dir, cfg, NewDiffChangeSource(dir))
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	writeWS(t, dir, "wiki/concepts/Secret.md", "minio secret")
	if _, err := m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{KeyFile: keyPath}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "wiki/concepts/Secret.md"))
	if string(b) != "minio secret" {
		t.Fatalf("Secret.md = %q", b)
	}
}
