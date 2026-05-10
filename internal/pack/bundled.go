package pack

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed bundled/*
var bundledFS embed.FS

// GetBundled loads a bundled pack by name from the embedded filesystem.
// Returns the manifest and a filesystem rooted at the pack directory.
func GetBundled(name string) (*PackManifest, fs.FS, error) {
	if err := ValidateName(name); err != nil {
		return nil, nil, fmt.Errorf("invalid bundled pack name: %w", err)
	}
	packDir := "bundled/" + name
	data, err := fs.ReadFile(bundledFS, packDir+"/pack.yaml")
	if err != nil {
		return nil, nil, fmt.Errorf("bundled pack %q not found", name)
	}

	var m PackManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, nil, fmt.Errorf("parsing bundled pack %q: %w", name, err)
	}

	sub, err := fs.Sub(bundledFS, packDir)
	if err != nil {
		return nil, nil, fmt.Errorf("accessing bundled pack %q: %w", name, err)
	}

	return &m, sub, nil
}

// IsBundled returns true if a pack with the given name is bundled.
func IsBundled(name string) bool {
	if ValidateName(name) != nil {
		return false
	}
	_, err := fs.ReadFile(bundledFS, "bundled/"+name+"/pack.yaml")
	return err == nil
}

// ListBundled returns metadata for all bundled packs.
func ListBundled() []PackInfo {
	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil
	}

	var packs []PackInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(bundledFS, "bundled/"+e.Name()+"/pack.yaml")
		if err != nil {
			continue
		}
		var m PackManifest
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue
		}
		packs = append(packs, PackInfo{
			Name:        m.Name,
			Version:     m.Version,
			Description: m.Description,
			Tier:        "bundled",
			Tags:        m.Tags,
		})
	}
	return packs
}

// InstallBundled extracts a bundled pack to the cache directory.
// This allows bundled packs to be installed offline.
func InstallBundled(name string, cacheDir string) (*PackManifest, string, error) {
	manifest, packFS, err := GetBundled(name)
	if err != nil {
		return nil, "", err
	}

	if cacheDir == "" {
		cacheDir = CacheDir()
	}

	destDir, err := extractEmbeddedPack(packFS, name, cacheDir)
	if err != nil {
		return nil, "", err
	}

	return manifest, destDir, nil
}

// extractEmbeddedPack writes an embedded pack FS to the cache directory.
func extractEmbeddedPack(packFS fs.FS, name string, cacheDir string) (string, error) {
	destDir := filepath.Join(cacheDir, name)
	tmpDest, err := os.MkdirTemp(cacheDir, ".pack-staging-*")
	if err != nil {
		if mkErr := os.MkdirAll(cacheDir, 0o755); mkErr != nil {
			return "", fmt.Errorf("creating cache dir: %w", mkErr)
		}
		tmpDest, err = os.MkdirTemp(cacheDir, ".pack-staging-*")
		if err != nil {
			return "", fmt.Errorf("creating temp cache dir: %w", err)
		}
	}

	if err := fs.WalkDir(packFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(tmpDest, path)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(packFS, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		os.RemoveAll(tmpDest)
		return "", fmt.Errorf("extracting bundled pack: %w", err)
	}

	backup := destDir + ".old"
	os.RemoveAll(backup)
	os.Rename(destDir, backup)
	if err := os.Rename(tmpDest, destDir); err != nil {
		os.Rename(backup, destDir)
		os.RemoveAll(tmpDest)
		return "", fmt.Errorf("atomic swap failed: %w", err)
	}
	os.RemoveAll(backup)
	return destDir, nil
}
