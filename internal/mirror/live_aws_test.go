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

// TestLiveAWS_RoundTrip is the maintainer-run live smoke (follow-up item
// 1b): enable → mutate+ship → verify → wipe → hydrate against a real
// bucket. Env-gated: SAGE_TEST_AWS=1 with real creds from env
// (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or the configured names),
// SAGE_TEST_AWS_BUCKET required, SAGE_TEST_AWS_ENDPOINT or
// SAGE_TEST_AWS_REGION. NEVER run in CI (no real cloud creds in tests).
func TestLiveAWS_RoundTrip(t *testing.T) {
	if os.Getenv("SAGE_TEST_AWS") != "1" {
		t.Skip("live-AWS smoke disabled (SAGE_TEST_AWS=1 with real creds to run — see docs/guides/remote-mirror.md)")
	}
	bucket := os.Getenv("SAGE_TEST_AWS_BUCKET")
	if bucket == "" {
		t.Fatal("SAGE_TEST_AWS_BUCKET required")
	}
	endpoint := os.Getenv("SAGE_TEST_AWS_ENDPOINT")
	region := os.Getenv("SAGE_TEST_AWS_REGION")
	if endpoint == "" {
		if region == "" {
			t.Fatal("SAGE_TEST_AWS_ENDPOINT or SAGE_TEST_AWS_REGION required")
		}
		endpoint = "https://s3." + region + ".amazonaws.com"
	}
	// SigV4 validates the scope region: "auto" is fine for R2/MinIO but AWS
	// 403s it — use the real region for AWS endpoints (F-022).
	signRegion := "auto"
	if region != "" {
		signRegion = region
	}
	cfg := Config{
		Endpoint: endpoint, Bucket: bucket, Prefix: "live-test/", Region: signRegion,
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('live')"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (creds/endpoint OK?): %v", err)
	}
	writeWS(t, dir, "wiki/concepts/Live.md", "# live")
	ageLocalRotationFile(t, dir, -2*time.Hour)
	if _, err := m.shipPass(context.Background()); err != nil {
		t.Fatalf("ship: %v", err)
	}
	rep, err := m.Verify(context.Background())
	if err != nil || !rep.Valid {
		t.Fatalf("verify: %+v %v", rep, err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki", "concepts", "Live.md")); err != nil {
		t.Fatal("Live.md not restored")
	}
}
