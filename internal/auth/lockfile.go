package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const staleThreshold = 120 * time.Second

func lockFile(path string) (unlock func(), err error) {
	lockPath := path + ".lock"

	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("auth: create lock dir: %w", err)
	}

	if info, statErr := os.Stat(lockPath); statErr == nil {
		if time.Since(info.ModTime()) > staleThreshold {
			os.Remove(lockPath)
		}
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("auth: could not acquire lock %s: %w", lockPath, err)
	}
	f.Close()

	return func() { os.Remove(lockPath) }, nil
}
