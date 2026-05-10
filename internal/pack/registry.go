package pack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultRegistryURL = "https://github.com/xoai/sage-wiki-packs"

// PackInfo holds metadata about a pack in the registry index.
type PackInfo struct {
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Tier        string   `yaml:"tier,omitempty"` // "official" or "community"
	MinVersion  string   `yaml:"min_version,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
}

// Registry manages access to the pack registry.
type Registry struct {
	CacheDir    string
	RegistryURL string
}

// NewRegistry creates a registry with default settings.
func NewRegistry(cacheDir string) *Registry {
	if cacheDir == "" {
		cacheDir = CacheDir()
	}
	return &Registry{
		CacheDir:    cacheDir,
		RegistryURL: defaultRegistryURL,
	}
}

// registryIndex is the on-disk format for index.yaml.
type registryIndex struct {
	Packs []PackInfo `yaml:"packs"`
}

func (r *Registry) cacheIndexPath() string {
	return filepath.Join(r.CacheDir, "registry-cache", "index.yaml")
}

func (r *Registry) cacheRepoPath() string {
	return filepath.Join(r.CacheDir, "registry-cache", "repo")
}

// FetchIndex clones or pulls the registry repo and parses the index.
// Falls back to stale cache on network failure.
func (r *Registry) FetchIndex() ([]PackInfo, error) {
	repoDir := r.cacheRepoPath()

	var fetchErr error
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		// repo exists — pull
		cmd := exec.Command("git", "-C", repoDir, "pull", "--ff-only")
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		fetchErr = cmd.Run()
	} else {
		// clone to temp then rename (atomic)
		if err := validateGitURL(r.RegistryURL); err != nil {
			return nil, err
		}
		tmpDir, err := os.MkdirTemp("", "sage-registry-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmpDir)

		cmd := exec.Command("git", "clone", "--depth=1", "--", r.RegistryURL, tmpDir)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		fetchErr = cmd.Run()

		if fetchErr == nil {
			os.MkdirAll(filepath.Dir(repoDir), 0o755)
			os.RemoveAll(repoDir)
			if err := os.Rename(tmpDir, repoDir); err != nil {
				if err := copyDir(tmpDir, repoDir); err != nil {
					return nil, fmt.Errorf("caching registry: %w", err)
				}
			}
		}
	}

	// try reading index from repo
	indexPath := filepath.Join(repoDir, "index.yaml")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if fetchErr != nil {
			// try stale cache
			return r.readStaleCache(fetchErr)
		}
		return nil, fmt.Errorf("reading registry index: %w", err)
	}

	var idx registryIndex
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing registry index: %w", err)
	}

	// cache the index for stale fallback
	cacheDir := filepath.Dir(r.cacheIndexPath())
	os.MkdirAll(cacheDir, 0o755)
	os.WriteFile(r.cacheIndexPath(), data, 0o644)

	return idx.Packs, nil
}

func (r *Registry) readStaleCache(fetchErr error) ([]PackInfo, error) {
	data, err := os.ReadFile(r.cacheIndexPath())
	if err != nil {
		return nil, fmt.Errorf("registry fetch failed (%v) and no stale cache available", fetchErr)
	}

	fmt.Fprintf(os.Stderr, "Warning: Registry fetch failed, using stale cache: %v\n", fetchErr)

	var idx registryIndex
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing stale cache: %w", err)
	}
	return idx.Packs, nil
}

// Search filters the registry index by query string (matches name, tags, description).
func (r *Registry) Search(query string) ([]PackInfo, error) {
	packs, err := r.FetchIndex()
	if err != nil {
		return nil, err
	}

	query = strings.ToLower(query)
	var results []PackInfo
	for _, p := range packs {
		if matchesPack(p, query) {
			results = append(results, p)
		}
	}
	return results, nil
}

func matchesPack(p PackInfo, query string) bool {
	if strings.Contains(strings.ToLower(p.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(p.Description), query) {
		return true
	}
	for _, tag := range p.Tags {
		if strings.Contains(strings.ToLower(tag), query) {
			return true
		}
	}
	return false
}

// InstallFromRegistry installs a pack by name from the registry.
// Always attempts to refresh the registry first; falls back to stale cache.
func (r *Registry) InstallFromRegistry(name string) (*PackManifest, string, error) {
	// always try to refresh to get the latest version
	if _, fetchErr := r.FetchIndex(); fetchErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: registry refresh failed, using cache: %v\n", fetchErr)
	}

	repoDir := r.cacheRepoPath()
	packDir := filepath.Join(repoDir, "packs", name)
	if _, err := os.Stat(filepath.Join(packDir, "pack.yaml")); err != nil {
		return nil, "", fmt.Errorf("pack %q not found in registry", name)
	}

	return Install(packDir, r.CacheDir)
}

// FindInIndex looks up a pack by name in the registry index.
func (r *Registry) FindInIndex(name string, packs []PackInfo) *PackInfo {
	for _, p := range packs {
		if p.Name == name {
			return &p
		}
	}
	return nil
}
