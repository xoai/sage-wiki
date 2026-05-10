package pack

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// PackState tracks installed packs for a project.
type PackState struct {
	Installed []InstalledPack `yaml:"installed"`
}

// InstalledPack records metadata about a single installed pack.
type InstalledPack struct {
	Name        string             `yaml:"name"`
	Version     string             `yaml:"version"`
	Source      string             `yaml:"source"`
	Priority    int                `yaml:"priority,omitempty"`
	InstalledAt time.Time          `yaml:"installed_at"`
	AppliedAt   time.Time          `yaml:"applied_at,omitempty"`
	Files       map[string]string  `yaml:"files,omitempty"`
	Snapshots   map[string]*string `yaml:"snapshots,omitempty"`
}

const stateFile = ".sage/pack-state.yaml"

// LoadState reads pack state from a project directory.
// Returns empty state if the file doesn't exist.
func LoadState(projectDir string) (*PackState, error) {
	path := filepath.Join(projectDir, stateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PackState{}, nil
		}
		return nil, fmt.Errorf("reading pack state: %w", err)
	}
	var s PackState
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing pack state: %w", err)
	}
	return &s, nil
}

// Save writes pack state to the project directory.
// Save writes pack state atomically (temp file + rename).
func (s *PackState) Save(projectDir string) error {
	path := filepath.Join(projectDir, stateFile)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshaling pack state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".pack-state-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temp state file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("atomic rename state file: %w", err)
	}
	return nil
}

// RecordInstall adds or updates an installed pack entry.
func (s *PackState) RecordInstall(name, version, source string, files map[string]string) {
	now := time.Now().UTC()
	for i, p := range s.Installed {
		if p.Name == name {
			s.Installed[i].Version = version
			s.Installed[i].Source = source
			s.Installed[i].AppliedAt = now
			s.Installed[i].Files = files
			return
		}
	}
	s.Installed = append(s.Installed, InstalledPack{
		Name:        name,
		Version:     version,
		Source:      source,
		InstalledAt: now,
		AppliedAt:   now,
		Files:       files,
	})
}

// FindInstalled returns the installed pack entry and its index, or -1 if not found.
func (s *PackState) FindInstalled(name string) (*InstalledPack, int) {
	for i, p := range s.Installed {
		if p.Name == name {
			return &s.Installed[i], i
		}
	}
	return nil, -1
}

// RemoveInstalled removes a pack from the installed list.
func (s *PackState) RemoveInstalled(name string) bool {
	for i, p := range s.Installed {
		if p.Name == name {
			s.Installed = append(s.Installed[:i], s.Installed[i+1:]...)
			return true
		}
	}
	return false
}

// IsModified checks if a file has been modified since the pack was applied.
func (s *PackState) IsModified(projectDir, packName, path string) bool {
	p, _ := s.FindInstalled(packName)
	if p == nil {
		return false
	}
	expected, ok := p.Files[path]
	if !ok {
		return false
	}
	actual, err := ComputeFileHash(filepath.Join(projectDir, path))
	if err != nil {
		return true // can't read = assume modified
	}
	return actual != expected
}

// SaveSnapshot copies pre-apply files to a snapshot directory for rollback.
// Files that don't exist get a nil hash marker.
func (s *PackState) SaveSnapshot(projectDir, packName string, paths []string) error {
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return err
	}

	snapshots := make(map[string]*string)
	for _, p := range paths {
		src := filepath.Join(projectDir, p)
		info, err := os.Stat(src)
		if err != nil {
			// file doesn't exist — record nil hash
			snapshots[p] = nil
			continue
		}
		if info.IsDir() {
			continue
		}
		dst := filepath.Join(snapshotDir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
		h, _ := ComputeFileHash(src)
		snapshots[p] = &h
	}

	// store snapshot hashes in state for the pack
	p, _ := s.FindInstalled(packName)
	if p != nil {
		p.Snapshots = snapshots
	} else {
		// temporarily store until RecordInstall merges
		s.Installed = append(s.Installed, InstalledPack{
			Name:      packName,
			Snapshots: snapshots,
		})
	}

	return nil
}

// RestoreSnapshot restores files from the snapshot directory.
// Files that had nil hash (didn't exist before) are deleted.
func (s *PackState) RestoreSnapshot(projectDir, packName string) error {
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName)

	p, _ := s.FindInstalled(packName)
	if p == nil {
		return nil
	}

	for path, hash := range p.Snapshots {
		dst := filepath.Join(projectDir, path)
		if hash == nil {
			// file didn't exist before — delete it
			os.Remove(dst)
			continue
		}
		src := filepath.Join(snapshotDir, path)
		data, err := os.ReadFile(src)
		if err != nil {
			continue // snapshot file missing — can't restore
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}

	return s.CleanSnapshot(projectDir, packName)
}

// CleanSnapshot removes the snapshot directory after successful apply.
func (s *PackState) CleanSnapshot(projectDir, packName string) error {
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName)
	return os.RemoveAll(snapshotDir)
}

// SaveTempSnapshot saves files to a temporary rollback directory for updates.
// This is separate from apply snapshots so updates don't destroy remove snapshots.
// Files that don't exist get a ".nil" marker so rollback knows to delete them.
func SaveTempSnapshot(projectDir, packName string, paths []string) error {
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName+"-update")
	os.RemoveAll(snapshotDir)
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return err
	}
	for _, p := range paths {
		src := filepath.Join(projectDir, p)
		dst := filepath.Join(snapshotDir, p)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		info, err := os.Stat(src)
		if err != nil || info.IsDir() {
			// file doesn't exist — write a nil marker so rollback deletes it
			if err := os.WriteFile(dst+".nil", []byte{}, 0o644); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RestoreTempSnapshot restores files from the temporary update rollback directory.
// Files with ".nil" markers are deleted (they didn't exist before the update).
func RestoreTempSnapshot(projectDir, packName string, paths []string) error {
	snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName+"-update")
	for _, p := range paths {
		dst := filepath.Join(projectDir, p)
		nilMarker := filepath.Join(snapshotDir, p+".nil")
		if _, err := os.Stat(nilMarker); err == nil {
			// file didn't exist before update — delete it
			os.Remove(dst)
			continue
		}
		src := filepath.Join(snapshotDir, p)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return CleanTempSnapshot(projectDir, packName)
}

// CleanTempSnapshot removes the temporary update rollback directory.
func CleanTempSnapshot(projectDir, packName string) error {
	return os.RemoveAll(filepath.Join(projectDir, ".sage", "pack-snapshots", packName+"-update"))
}

// ComputeFileHash returns the SHA-256 hex digest of a file.
func ComputeFileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}
