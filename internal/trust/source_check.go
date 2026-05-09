package trust

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func ComputeSourcesHash(projectDir string, sourcesJSON string) string {
	if sourcesJSON == "" {
		return ""
	}
	var paths []string
	if err := json.Unmarshal([]byte(sourcesJSON), &paths); err != nil {
		return ""
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		absPath := filepath.Join(projectDir, p)
		data, err := os.ReadFile(absPath)
		if err != nil {
			h.Write([]byte("MISSING:" + p))
			continue
		}
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil)[:16])
}

func CheckSourceChanges(store *Store, projectDir string, stores *IndexStores) (int, error) {
	confirmed, err := store.ListConfirmed()
	if err != nil {
		return 0, err
	}

	demoted := 0
	for _, o := range confirmed {
		if o.SourcesHash == "" {
			continue
		}
		currentHash := ComputeSourcesHash(projectDir, o.SourcesUsed)
		if currentHash != o.SourcesHash {
			if stores != nil {
				if err := DemoteOutput(store, o.ID, *stores); err != nil {
					return demoted, fmt.Errorf("demote %s: %w", o.ID, err)
				}
			} else {
				if err := store.Demote(o.ID); err != nil {
					return demoted, fmt.Errorf("demote %s: %w", o.ID, err)
				}
			}
			demoted++
		}
	}
	return demoted, nil
}
