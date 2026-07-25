package pack

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CacheDir returns the default pack cache directory.
func CacheDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sage-wiki", "packs")
}

// Install downloads or copies a pack to the local cache.
// nameOrURL can be a bundled pack name, a Git URL, or a local directory path.
// Bundled packs are checked first, then local paths, then Git URLs.
// Returns the loaded manifest and the cache path.
func Install(nameOrURL string, cacheDir string) (*PackManifest, string, error) {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}

	if IsBundled(nameOrURL) {
		return InstallBundled(nameOrURL, cacheDir)
	}
	if isLocalPath(nameOrURL) {
		return installFromLocal(nameOrURL, cacheDir)
	}
	return installFromGit(nameOrURL, cacheDir)
}

// IsInstalled checks if a pack exists in the cache.
func IsInstalled(name string, cacheDir string) bool {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}
	_, err := os.Stat(filepath.Join(cacheDir, name, "pack.yaml"))
	return err == nil
}

// InstalledPath returns the cache path for an installed pack.
func InstalledPath(name string, cacheDir string) string {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}
	return filepath.Join(cacheDir, name)
}

func isLocalPath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return true
	}
	if _, err := os.Stat(s); err == nil {
		return true
	}
	return false
}

// validateGitURL rejects dangerous git URL schemes.
func validateGitURL(url string) error {
	if strings.HasPrefix(url, "-") {
		return fmt.Errorf("unsafe git URL: %q (looks like a flag)", url)
	}
	if strings.HasPrefix(url, "ext::") {
		return fmt.Errorf("unsafe git URL: %q (ext protocol allows arbitrary command execution)", url)
	}
	lower := strings.ToLower(url)
	if !strings.HasPrefix(lower, "https://") &&
		!strings.HasPrefix(lower, "http://") &&
		!strings.HasPrefix(lower, "git://") &&
		!strings.HasPrefix(lower, "ssh://") &&
		!strings.Contains(lower, "@") {
		return fmt.Errorf("unsupported git URL scheme: %q (use https://, git://, or ssh://)", url)
	}
	return nil
}

func installFromLocal(localDir string, cacheDir string) (*PackManifest, string, error) {
	absDir, err := filepath.Abs(localDir)
	if err != nil {
		return nil, "", fmt.Errorf("resolving path: %w", err)
	}

	manifest, err := LoadManifest(absDir)
	if err != nil {
		return nil, "", fmt.Errorf("invalid pack at %s: %w", localDir, err)
	}

	if manifest.MinVersion != "" {
		if err := CheckMinVersion(manifest.MinVersion); err != nil {
			return nil, "", err
		}
	}

	destDir := filepath.Join(cacheDir, manifest.Name)

	// atomic replacement: copy to temp sibling, then rename into place
	if err := atomicCacheReplace(absDir, destDir, cacheDir); err != nil {
		return nil, "", fmt.Errorf("copying pack to cache: %w", err)
	}

	return manifest, destDir, nil
}

func installFromGit(url string, cacheDir string) (*PackManifest, string, error) {
	if err := validateGitURL(url); err != nil {
		return nil, "", err
	}

	tmpDir, err := os.MkdirTemp("", "sage-wiki-pack-*")
	if err != nil {
		return nil, "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("git", "clone", "--depth=1", "--", url, tmpDir)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("git clone %s: %w", url, err)
	}

	os.RemoveAll(filepath.Join(tmpDir, ".git"))

	manifest, err := LoadManifest(tmpDir)
	if err != nil {
		return nil, "", fmt.Errorf("invalid pack at %s: %w", url, err)
	}

	if manifest.MinVersion != "" {
		if err := CheckMinVersion(manifest.MinVersion); err != nil {
			return nil, "", err
		}
	}

	destDir := filepath.Join(cacheDir, manifest.Name)

	// atomic replacement: copy to temp sibling (with symlink filtering),
	// then rename into place
	if err := atomicCacheReplace(tmpDir, destDir, cacheDir); err != nil {
		return nil, "", fmt.Errorf("copying pack to cache: %w", err)
	}

	return manifest, destDir, nil
}

// atomicCacheReplace copies src to a temp sibling of dest, validates,
// then atomically swaps into place. The old cache entry is kept until
// the replacement is complete.
func atomicCacheReplace(src, dest, cacheDir string) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	tmpDest, err := os.MkdirTemp(cacheDir, ".pack-staging-*")
	if err != nil {
		return fmt.Errorf("creating temp cache dir: %w", err)
	}

	if err := copyDir(src, tmpDest); err != nil {
		os.RemoveAll(tmpDest)
		return err
	}

	// verify the copy has a valid manifest before swapping
	if _, err := LoadManifest(tmpDest); err != nil {
		os.RemoveAll(tmpDest)
		return fmt.Errorf("staged copy is invalid: %w", err)
	}

	// swap: rename old to backup, rename new into place, remove old
	backup := dest + ".old"
	os.RemoveAll(backup)
	os.Rename(dest, backup) // may not exist on first install
	if err := os.Rename(tmpDest, dest); err != nil {
		// rename failed — restore old and clean up
		os.Rename(backup, dest)
		os.RemoveAll(tmpDest)
		return fmt.Errorf("atomic swap failed: %w", err)
	}
	os.RemoveAll(backup)
	return nil
}

// containedWithin reports whether realPath is inside absSrc, with a
// separator boundary so a sibling like /x/pack2 never passes a check
// rooted at /x/pack (the codebase's containment idiom).
func containedWithin(realPath, absSrc string) bool {
	return realPath == absSrc ||
		strings.HasPrefix(realPath, absSrc+string(os.PathSeparator))
}

// copyDir copies a directory tree, skipping symlinks to prevent
// symlink-based path traversal.
func copyDir(src, dst string) error {
	absSrc, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	// Resolve the base too: walk entries are EvalSymlinks-resolved below,
	// and comparing them against an UNRESOLVED base fails whenever src
	// sits under a symlinked parent (macOS tempdir /var -> /private/var —
	// the macOS CI failure).
	if absSrc, err = filepath.EvalSymlinks(absSrc); err != nil {
		return err
	}

	// NOTE: if the copy ROOT itself is a symlink, WalkDir Lstat's it as
	// ModeSymlink and this function copies nothing (silent no-op). That
	// is acceptable for pack inputs (a symlinked pack root is rejected
	// upstream by the symlink-skip rule's design), but do not call copyDir
	// with a symlinked src expecting content.
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// skip symlinks entirely — prevents reading outside the pack
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		// for regular files, verify the real path stays within src
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if !containedWithin(realPath, absSrc) {
			return fmt.Errorf("path %q resolves outside pack directory", path)
		}

		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// preserve source permissions (especially executable bits for parsers)
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, data, mode)
	})
}
