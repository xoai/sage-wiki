package parity

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xoai/sage-wiki/internal/mirror"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// TestMirrorRoundtrip is AC-1: the parity-suite workspace mirrored, wiped,
// and hydrated restores byte-identical parity (spec: golden corpus → mirror
// → wipe → hydrate → parity passes on the restored dir). Uses an in-test
// fake S3 so the standard offline suite covers it (no MinIO required).

type parityFakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newParityFakeS3() *parityFakeS3 { return &parityFakeS3{objects: map[string][]byte{}} }

func (f *parityFakeS3) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bk/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			f.objects[key] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				var sb strings.Builder
				sb.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
				for k := range f.objects {
					if strings.HasPrefix(k, prefix) {
						sb.WriteString("<Contents><Key>" + k + "</Key></Contents>")
					}
				}
				sb.WriteString(`</ListBucketResult>`)
				w.Write([]byte(sb.String()))
				return
			}
			if b, ok := f.objects[key]; ok {
				w.Write(b)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodHead:
			if _, ok := f.objects[key]; ok {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodDelete:
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func copyWorkspaceTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		// Skip the lock files — a copied workspace must not carry another
		// process's locks.
		base := info.Name()
		if base == "engine.lock" || base == "mirror-ship.lock" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMirrorRoundtrip(t *testing.T) {
	if suiteWS == "" {
		t.Skip("parity suite workspace unavailable (SAGE_PARITY_FORCE=1)")
	}
	fake := newParityFakeS3()
	srv := fake.server()
	defer srv.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")

	// Copy the suite workspace (do not disturb the shared one).
	workDir := filepath.Join(t.TempDir(), "ws")
	copyWorkspaceTree(t, suiteWS, workDir)

	cfg := mirror.Config{
		Endpoint: srv.URL, Bucket: "bk", Prefix: "ws/", Region: "auto",
	}
	m, err := mirror.Open(workDir, cfg, mirror.NewDiffChangeSource(workDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	// Ship everything (objects + any pending db writes).
	if err := m.Ship(context.Background(), pkmirror.ChangeBatch{}); err != nil {
		t.Fatalf("ship: %v", err)
	}

	// Wipe → hydrate → parity on the restored dir.
	restored := filepath.Join(t.TempDir(), "restored")
	if _, err := mirror.Hydrate(context.Background(), cfg, restored, mirror.HydrateOpts{}); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}

	if err := CheckByteParity(restored, goldenConfigPath(), goldenPath("byte-parity.json")); err != nil {
		t.Fatalf("byte parity on restored dir: %v", err)
	}
}
