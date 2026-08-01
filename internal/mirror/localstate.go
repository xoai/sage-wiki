package mirror

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LocalState is .sage/mirror-local.json — machine-local ship bookkeeping,
// NEVER shipped (spec.md §Key decisions 7). All fields are the shipper's
// private bookkeeping; the bucket is the only shared truth.
type LocalState struct {
	Generation        int       `json:"generation"`
	WALSalt           uint64    `json:"wal_salt"`
	WALOffset         int64     `json:"wal_offset"`
	LastDBSHA256      string    `json:"last_db_sha256"`
	LastDBSize        int64     `json:"last_db_size"`
	LastSegmentSeq    int       `json:"last_segment_seq"`
	LastRotationAt    time.Time `json:"last_rotation_at"`
	PendingRotation   bool      `json:"pending_rotation"`
	ConsecutiveDefers int       `json:"consecutive_defers"`
}

// LoadLocalState reads the file; a missing file is a FRESH zero state (first
// run), a corrupt file is a loud error (never a silent reset — principle 2).
func LoadLocalState(path string) (*LocalState, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &LocalState{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mirror-local: read: %w", err)
	}
	var s LocalState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("mirror-local: parse %s: %w", path, err)
	}
	return &s, nil
}

// SaveLocalState writes atomically: temp file in the same dir + rename, so a
// crash never leaves a partially written state (manifest.Save pattern).
func SaveLocalState(path string, s *LocalState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("mirror-local: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mirror-local: create dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("mirror-local: write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("mirror-local: rename: %w", err)
	}
	return nil
}
