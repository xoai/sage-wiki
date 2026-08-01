package mirror

import (
	"context"
	"errors"
	"testing"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// Compile-time conformance: the concrete Mirror implements the SPEC-01 seam.
var _ pkmirror.Mirror = (*Mirror)(nil)

type fakeOps struct {
	enabled   bool
	shipErr   error
	snapID    pkmirror.SnapshotID
	snapErr   error
	statusOut Status
	verReport Report
	hydrateTo string
}

func (f *fakeOps) Enable(ctx context.Context) error                       { f.enabled = true; return nil }
func (f *fakeOps) Ship(ctx context.Context, b pkmirror.ChangeBatch) error { return f.shipErr }
func (f *fakeOps) Snapshot(ctx context.Context) (pkmirror.SnapshotID, error) {
	return f.snapID, f.snapErr
}
func (f *fakeOps) Status(ctx context.Context) (Status, error) { return f.statusOut, nil }
func (f *fakeOps) VerifyMode(ctx context.Context, fast bool) (Report, error) {
	return f.verReport, nil
}
func (f *fakeOps) Hydrate(ctx context.Context, dst string) error { f.hydrateTo = dst; return nil }

func TestMirrorFacade_Conformance(t *testing.T) {
	// The var _ assertion above is the conformance proof; this guards
	// against a future relaxation of the binding.
	var iface pkmirror.Mirror = &Mirror{}
	if iface == nil {
		t.Fatal("Mirror must implement pkg/mirror.Mirror")
	}
}

func TestMirrorFacade_Delegates(t *testing.T) {
	f := &fakeOps{snapID: "gen-42"}
	m := &Mirror{ops: f}
	ctx := context.Background()

	if err := m.Enable(ctx); err != nil || !f.enabled {
		t.Fatalf("Enable not delegated: %v %v", err, f.enabled)
	}
	if err := m.Ship(ctx, pkmirror.ChangeBatch{ID: "b1"}); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	id, err := m.Snapshot(ctx)
	if err != nil || id != "gen-42" {
		t.Fatalf("Snapshot = %q, %v", id, err)
	}
	if err := m.Hydrate(ctx, "/tmp/dst"); err != nil || f.hydrateTo != "/tmp/dst" {
		t.Fatalf("Hydrate not delegated: %v %q", err, f.hydrateTo)
	}
}

func TestMirrorFacade_ShipErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	m := &Mirror{ops: &fakeOps{shipErr: want}}
	if err := m.Ship(context.Background(), pkmirror.ChangeBatch{}); !errors.Is(err, want) {
		t.Fatalf("Ship err = %v, want %v", err, want)
	}
}

func TestOpen_BadCreds(t *testing.T) {
	cfg := testConfig(t)
	cfg.AccessKeyEnv = "UNSET_MIRROR_TEST_AK"
	cfg.SecretKeyEnv = "UNSET_MIRROR_TEST_SK"
	if _, err := Open(t.TempDir(), cfg, nil); err == nil {
		t.Fatal("Open with missing creds should error")
	}
}

func TestOpen_Success(t *testing.T) {
	t.Setenv("MIRROR_TEST_AK", "ak")
	t.Setenv("MIRROR_TEST_SK", "sk")
	cfg := testConfig(t)
	cfg.AccessKeyEnv = "MIRROR_TEST_AK"
	cfg.SecretKeyEnv = "MIRROR_TEST_SK"
	m, err := Open(t.TempDir(), cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if m == nil || m.client == nil {
		t.Fatal("Open returned Mirror without s3 client")
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Endpoint: "http://localhost:9000",
		Bucket:   "b",
		Prefix:   "ws/",
		Region:   "auto",
	}
}

var _ = s3.Credentials{}
