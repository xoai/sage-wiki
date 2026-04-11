package compiler

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
)

// Watch monitors source directories for changes and triggers compilation.
// It tries fsnotify first, then falls back to polling if no events are
// received (common on WSL2 /mnt/ paths and network drives).
func Watch(projectDir string, debounceSeconds int) error {
	if debounceSeconds <= 0 {
		debounceSeconds = 2
	}

	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	sourcePaths := cfg.ResolveSources(projectDir)

	// Run initial compile to catch files added while watcher was stopped
	log.Info("running initial compile before watching")
	if result, err := Compile(projectDir, CompileOpts{}); err != nil {
		log.Error("initial compile failed", "error", err)
	} else if result.Added > 0 || result.Modified > 0 || result.Removed > 0 {
		log.Info("initial compile complete",
			"added", result.Added,
			"summarized", result.Summarized,
			"concepts", result.ConceptsExtracted,
			"articles", result.ArticlesWritten,
		)
	} else {
		log.Info("initial compile: nothing new to process")
	}

	// Try fsnotify first
	watcher, fsErr := fsnotify.NewWatcher()
	if fsErr == nil {
		for _, sp := range sourcePaths {
			addRecursive(watcher, sp)
		}
	}

	// Start polling as primary or fallback
	// On WSL2 with /mnt/ paths, fsnotify silently fails to deliver events
	usePolling := fsErr != nil
	if !usePolling {
		// Detect if we're on a path where inotify won't work
		for _, sp := range sourcePaths {
			if strings.HasPrefix(sp, "/mnt/") {
				usePolling = true
				break
			}
		}
	}

	if usePolling {
		if watcher != nil {
			watcher.Close()
		}
		log.Info("using polling mode (inotify unavailable for these paths)", "sources", sourcePaths, "interval", fmt.Sprintf("%ds", debounceSeconds*2))
		return watchPoll(projectDir, sourcePaths, cfg.Ignore, debounceSeconds*2)
	}

	defer watcher.Close()
	log.Info("watching for changes (fsnotify)", "sources", sourcePaths, "debounce", debounceSeconds)
	return watchFsnotify(projectDir, watcher, debounceSeconds)
}

// watchFsnotify uses inotify-based watching (works on native Linux, macOS).
func watchFsnotify(projectDir string, watcher *fsnotify.Watcher, debounceSeconds int) error {
	debounce := time.Duration(debounceSeconds) * time.Second
	var timer *time.Timer
	var compileMu sync.Mutex
	var lastTrigger string

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}

			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}

			log.Info("file change detected", "path", event.Name, "op", event.Op.String())

			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					watcher.Add(event.Name)
				}
			}

			lastTrigger = event.Name

			if timer != nil {
				timer.Stop()
			}
			trigger := lastTrigger
			timer = time.AfterFunc(debounce, func() {
				triggerCompile(projectDir, trigger, &compileMu)
			})

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Error("watcher error", "error", err)
		}
	}
}

// watchPoll periodically scans source directories for changes.
// Works on WSL2 /mnt/ paths, network drives, and any filesystem.
func watchPoll(projectDir string, sourcePaths []string, ignore []string, intervalSeconds int) error {
	var compileMu sync.Mutex

	// Build initial snapshot
	snapshot := scanSnapshot(sourcePaths, ignore)
	log.Info("initial snapshot", "files", len(snapshot))

	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		newSnapshot := scanSnapshot(sourcePaths, ignore)

		// Detect changes
		var changed []string

		// New or modified files
		for path, hash := range newSnapshot {
			if oldHash, exists := snapshot[path]; !exists {
				changed = append(changed, path)
				log.Info("new file detected", "path", path)
			} else if oldHash != hash {
				changed = append(changed, path)
				log.Info("file modified", "path", path)
			}
		}

		// Deleted files
		for path := range snapshot {
			if _, exists := newSnapshot[path]; !exists {
				changed = append(changed, path)
				log.Info("file removed", "path", path)
			}
		}

		snapshot = newSnapshot

		if len(changed) > 0 {
			log.Info("changes detected", "count", len(changed))
			triggerCompile(projectDir, changed[0], &compileMu)
		}
	}

	return nil
}

// scanSnapshot builds a map of file path → content hash for all source files.
func scanSnapshot(sourcePaths []string, ignore []string) map[string]string {
	snapshot := make(map[string]string)

	for _, dir := range sourcePaths {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}

			// Use relative path for consistent ignore matching with Diff
			relPath, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				relPath = path
			}
			if isIgnored(relPath, ignore) {
				return nil
			}

			hash := quickHash(path)
			if hash != "" {
				snapshot[path] = hash
			}
			return nil
		})
	}

	return snapshot
}

// quickHash returns a fast hash of file metadata (size + modtime).
// Avoids reading file contents for performance on large vaults.
func quickHash(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// Use size + modtime as a fast change indicator
	return fmt.Sprintf("%d-%d", info.Size(), info.ModTime().UnixNano())
}

// fullHash reads the file and returns SHA-256 (used when content comparison needed).
func fullHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func triggerCompile(projectDir string, trigger string, compileMu *sync.Mutex) {
	if !compileMu.TryLock() {
		log.Info("compile already in progress, skipping", "trigger", trigger)
		return
	}
	defer compileMu.Unlock()

	log.Info("compiling after change", "trigger", trigger)
	result, err := Compile(projectDir, CompileOpts{})
	if err != nil {
		log.Error("compile failed", "error", err)
	} else {
		log.Info("compile complete",
			"summarized", result.Summarized,
			"concepts", result.ConceptsExtracted,
			"articles", result.ArticlesWritten,
		)
	}
}

// addRecursive adds a directory and all subdirectories to the watcher.
func addRecursive(watcher *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if err := watcher.Add(path); err != nil {
				return err
			}
		}
		return nil
	})
}
