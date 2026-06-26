package compiler

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
)

// Watch monitors source directories for changes and triggers compilation.
// It tries fsnotify first, then falls back to polling if no events are
// received (common on WSL2 /mnt/ paths and network drives).
// If coordinator is non-nil, it is used to serialize compiles with on-demand
// requests. If nil, a local coordinator is created.
func Watch(projectDir string, debounceSeconds int, opts CompileOpts, coordinator ...*CompileCoordinator) error {
	var cc *CompileCoordinator
	if len(coordinator) > 0 && coordinator[0] != nil {
		cc = coordinator[0]
	} else {
		cc = NewCompileCoordinator()
	}
	if debounceSeconds <= 0 {
		debounceSeconds = 2
	}

	// D5: reject watch when a pending batch exists on disk
	statePath := filepath.Join(projectDir, ".sage", "compile-state.json")
	if state, _ := loadCompileState(statePath); state != nil && state.Batch != nil {
		return fmt.Errorf("pending batch compile detected; run 'sage-wiki compile' to complete it before starting watch mode")
	}

	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	sourcePaths := cfg.ResolveSources(projectDir)

	// Run initial compile with full opts (including Fresh if set)
	log.Info("running initial compile before watching")
	if result, err := Compile(projectDir, opts); err != nil {
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

	// D4: subsequent triggered compiles never use Fresh
	triggerOpts := opts
	triggerOpts.Fresh = false

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
		return watchPoll(projectDir, cfg.Sources, cfg.Ignore, debounceSeconds*2, triggerOpts, cc)
	}

	defer watcher.Close()
	log.Info("watching for changes (fsnotify)", "sources", sourcePaths, "debounce", debounceSeconds)
	return watchFsnotify(projectDir, watcher, debounceSeconds, triggerOpts, cc)
}

// watchFsnotify uses inotify-based watching (works on native Linux, macOS).
func watchFsnotify(projectDir string, watcher *fsnotify.Watcher, debounceSeconds int, opts CompileOpts, cc *CompileCoordinator) error {
	debounce := time.Duration(debounceSeconds) * time.Second
	var timer *time.Timer
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
				triggerCompile(projectDir, trigger, opts, cc)
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
func watchPoll(projectDir string, sources []config.Source, ignore []string, intervalSeconds int, opts CompileOpts, cc *CompileCoordinator) error {

	// Build initial snapshot
	snapshot := scanSnapshot(projectDir, sources, ignore)
	log.Info("initial snapshot", "files", len(snapshot))

	ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		newSnapshot := scanSnapshot(projectDir, sources, ignore)

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
			triggerCompile(projectDir, changed[0], opts, cc)
		}
	}

	return nil
}

// scanSnapshot builds a map of file path → content hash for all source files.
// Uses WalkSourceDir so symlinked source directories are followed correctly.
// Ignore matching uses manifest paths (source-name/file-path) for consistency
// with Diff.
func scanSnapshot(projectDir string, sources []config.Source, ignore []string) map[string]string {
	snapshot := make(map[string]string)

	for _, src := range sources {
		var srcDir string
		if filepath.IsAbs(src.Path) {
			srcDir = src.Path
		} else {
			srcDir = filepath.Join(projectDir, src.Path)
		}

		WalkSourceDir(srcDir, func(absPath, relPath string, _ os.DirEntry) error {
			manifestPath := filepath.ToSlash(filepath.Join(src.Path, relPath))
			if filepath.IsAbs(manifestPath) {
				if rel, err := filepath.Rel(projectDir, manifestPath); err == nil {
					manifestPath = filepath.ToSlash(rel)
				}
			}
			if isIgnored(manifestPath, ignore) {
				return nil
			}

			hash := quickHash(absPath)
			if hash != "" {
				snapshot[absPath] = hash
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

var compileFn = Compile

func triggerCompile(projectDir string, trigger string, opts CompileOpts, cc *CompileCoordinator) {
	ok, err := cc.TryCompile(func() error {
		log.Info("compiling after change", "trigger", trigger)
		result, err := compileFn(projectDir, opts)
		if err != nil {
			log.Error("compile failed", "error", err)
			return err
		}
		log.Info("compile complete",
			"summarized", result.Summarized,
			"concepts", result.ConceptsExtracted,
			"articles", result.ArticlesWritten,
		)
		return nil
	})
	if !ok {
		log.Info("compile already in progress, skipping", "trigger", trigger)
	}
	if err != nil {
		log.Error("triggered compile error", "error", err)
	}
}

// addRecursive adds a directory and all subdirectories to the watcher.
func addRecursive(watcher *fsnotify.Watcher, dir string) error {
	walkPath := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		walkPath = resolved
	} else {
		log.Warn("addRecursive: failed to resolve symlinks, watching as-is",
			"path", dir, "error", err)
	}
	return filepath.WalkDir(walkPath, func(path string, d os.DirEntry, err error) error {
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
