package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ageLocalRotationFile backdates last_rotation_at in mirror-local.json
// (debounce control; aging m.local in memory does NOT work — shipPass
// reloads the file under the mutex).
func ageLocalRotationFile(t *testing.T, dir string, delta time.Duration) {
	t.Helper()
	path := filepath.Join(dir, ".sage", "mirror-local.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	marker := `"last_rotation_at"`
	start := strings.Index(s, marker)
	if start < 0 {
		t.Fatal("no last_rotation_at")
	}
	tail := s[start:]
	end := strings.Index(tail, "\n")
	if end < 0 {
		end = len(tail)
	}
	replaced := `"last_rotation_at": "` + time.Now().Add(delta).UTC().Format(time.RFC3339) + `",` + tail[end:]
	if err := os.WriteFile(path, []byte(s[:start]+replaced), 0o644); err != nil {
		t.Fatal(err)
	}
}
