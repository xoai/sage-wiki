package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpdateAvailable describes a pack with a newer version in the registry.
type UpdateAvailable struct {
	Name           string
	CurrentVersion string
	LatestVersion  string
}

// UpdateResult summarizes what happened during a pack update.
type UpdateResult struct {
	Updated       []string
	Skipped       []string
	Conflicts     []string
	ConfigChanges []string
	OntologyAdded []string
}

// CheckUpdates compares installed pack versions against the registry index.
func CheckUpdates(state *PackState, packs []PackInfo) []UpdateAvailable {
	var updates []UpdateAvailable
	for _, installed := range state.Installed {
		for _, p := range packs {
			if p.Name == installed.Name && compareSemver(p.Version, installed.Version) > 0 {
				updates = append(updates, UpdateAvailable{
					Name:           installed.Name,
					CurrentVersion: installed.Version,
					LatestVersion:  p.Version,
				})
			}
		}
	}
	return updates
}

// UpdatePack fetches the latest version and applies changes transactionally.
// Modified files are skipped. Config and ontology changes are applied.
// On failure, all file changes are rolled back via snapshots.
func UpdatePack(name, projectDir string, state *PackState, registry *Registry) (*UpdateResult, error) {
	installed, _ := state.FindInstalled(name)
	if installed == nil {
		return nil, fmt.Errorf("pack %q is not installed", name)
	}

	// enforce source boundary: only update packs installed from registry
	if installed.Source != "registry" {
		return nil, fmt.Errorf("pack %q was installed from %q; only registry-installed packs can be updated via pack update (reinstall from source to update)", name, installed.Source)
	}

	manifest, packDir, err := registry.InstallFromRegistry(name)
	if err != nil {
		return nil, fmt.Errorf("fetching latest %s: %w", name, err)
	}

	if err := validateManifestPaths(manifest); err != nil {
		return nil, fmt.Errorf("unsafe pack manifest in update: %w", err)
	}

	result := &UpdateResult{}

	// validate all state-derived paths before use
	absProjectDir, _ := filepath.Abs(projectDir)
	absProjectDir, _ = filepath.EvalSymlinks(absProjectDir)
	for path := range installed.Files {
		if err := ValidateRelPath(path); err != nil {
			return nil, fmt.Errorf("unsafe path in pack state: %s: %w", path, err)
		}
		fullPath := filepath.Join(projectDir, path)
		parentDir := filepath.Dir(fullPath)
		resolvedParent, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			resolvedParent, _ = filepath.Abs(parentDir)
		}
		resolvedFull := filepath.Join(resolvedParent, filepath.Base(fullPath))
		if !strings.HasPrefix(resolvedFull, absProjectDir+string(filepath.Separator)) && resolvedFull != absProjectDir {
			return nil, fmt.Errorf("pack state path %q resolves outside project", path)
		}
	}

	// identify modified files before update
	modifiedFiles := make(map[string]bool)
	for path := range installed.Files {
		if state.IsModified(projectDir, name, path) {
			modifiedFiles[path] = true
			result.Conflicts = append(result.Conflicts, path)
		}
	}

	// collect all paths that will be written for snapshotting
	var pathsToSnapshot []string
	pathsToSnapshot = append(pathsToSnapshot, "config.yaml")
	for _, p := range manifest.Prompts {
		rp := filepath.Join("prompts", p)
		if !modifiedFiles[rp] {
			pathsToSnapshot = append(pathsToSnapshot, rp)
		}
	}
	for _, s := range manifest.Skills {
		rp := filepath.Join("skills", s)
		if !modifiedFiles[rp] {
			pathsToSnapshot = append(pathsToSnapshot, rp)
		}
	}
	for _, s := range manifest.Samples {
		rp := filepath.Join("raw", s)
		if !modifiedFiles[rp] {
			pathsToSnapshot = append(pathsToSnapshot, rp)
		}
	}
	for _, p := range manifest.Parsers {
		rp := filepath.Join("parsers", p)
		if !modifiedFiles[rp] {
			pathsToSnapshot = append(pathsToSnapshot, rp)
		}
	}

	// also snapshot old owned files that may be deleted as obsolete
	snapshotted := make(map[string]bool)
	for _, p := range pathsToSnapshot {
		snapshotted[p] = true
	}
	for path := range installed.Files {
		if !snapshotted[path] {
			pathsToSnapshot = append(pathsToSnapshot, path)
		}
	}

	// use a SEPARATE temp snapshot for update rollback, preserving the
	// original apply snapshots that pack remove needs
	if err := SaveTempSnapshot(projectDir, name, pathsToSnapshot); err != nil {
		return nil, fmt.Errorf("snapshot pre-update state: %w", err)
	}

	var updateErr error
	defer func() {
		if updateErr != nil {
			_ = RestoreTempSnapshot(projectDir, name, pathsToSnapshot)
		} else {
			_ = CleanTempSnapshot(projectDir, name)
		}
	}()

	// apply config and ontology changes only if config.yaml is unmodified
	hasConfig := manifest.Config != nil || len(manifest.ArticleFields) > 0
	hasOntology := len(manifest.Ontology.RelationTypes) > 0 || len(manifest.Ontology.EntityTypes) > 0
	configModified := modifiedFiles["config.yaml"]
	configUpdated := false
	_, previouslyOwnedConfig := installed.Files["config.yaml"]
	if (hasConfig || hasOntology) && !configModified {
		// if taking first ownership of config.yaml, save a remove snapshot
		// abort if snapshot can't be saved — prevents unrestorable config
		if !previouslyOwnedConfig {
			if installed.Snapshots == nil {
				installed.Snapshots = make(map[string]*string)
			}
			configPath := filepath.Join(projectDir, "config.yaml")
			if _, statErr := os.Stat(configPath); statErr == nil {
				snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", name)
				if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
					updateErr = fmt.Errorf("creating config snapshot dir: %w", err)
					return nil, updateErr
				}
				data, err := os.ReadFile(configPath)
				if err != nil {
					updateErr = fmt.Errorf("reading config for snapshot: %w", err)
					return nil, updateErr
				}
				if err := os.WriteFile(filepath.Join(snapshotDir, "config.yaml"), data, 0o644); err != nil {
					updateErr = fmt.Errorf("writing config snapshot: %w", err)
					return nil, updateErr
				}
				h, _ := ComputeFileHash(configPath)
				installed.Snapshots["config.yaml"] = &h
			}
		}
		// use replace merge when pack previously owned config — allows
		// pack config updates to override old pack values
		configMode := ModeMerge
		if previouslyOwnedConfig {
			configMode = ModeReplace
		}
		applyResult := &ApplyResult{}
		updateErr = applyConfigAndOntology(projectDir, manifest, configMode, applyResult)
		if updateErr != nil {
			return nil, fmt.Errorf("update config/ontology: %w", updateErr)
		}
		result.ConfigChanges = applyResult.ConfigChanges
		result.OntologyAdded = applyResult.OntologyAdded
		configUpdated = true
	} else if (hasConfig || hasOntology) && configModified {
		result.Skipped = append(result.Skipped, "config.yaml (user-modified, config/ontology changes skipped)")
	}

	// build set of paths this pack previously owned
	ownedPaths := make(map[string]bool, len(installed.Files))
	for path := range installed.Files {
		ownedPaths[path] = true
	}

	// copy unmodified files, detecting conflicts with unowned project files
	newFiles := make(map[string]string)

	type fileCopy struct {
		src, dst, relPath string
	}
	var copies []fileCopy

	addCopy := func(rp, src, dst string) {
		if modifiedFiles[rp] {
			result.Skipped = append(result.Skipped, rp)
			return
		}
		// new file in this version: check if project already has it (unowned)
		if !ownedPaths[rp] {
			if _, err := os.Stat(dst); err == nil {
				result.Conflicts = append(result.Conflicts, rp+" (unowned project file)")
				result.Skipped = append(result.Skipped, rp)
				return
			}
		}
		copies = append(copies, fileCopy{src: src, dst: dst, relPath: rp})
	}

	for _, p := range manifest.Prompts {
		rp := filepath.Join("prompts", p)
		addCopy(rp, filepath.Join(packDir, "prompts", p), filepath.Join(projectDir, "prompts", p))
	}
	for _, s := range manifest.Skills {
		rp := filepath.Join("skills", s)
		addCopy(rp, filepath.Join(packDir, "skills", s), filepath.Join(projectDir, "skills", s))
	}
	for _, s := range manifest.Samples {
		rp := filepath.Join("raw", s)
		addCopy(rp, filepath.Join(packDir, "samples", s), filepath.Join(projectDir, "raw", s))
	}
	// only update parser files if the pack previously owned any parser paths
	hadParsers := false
	for path := range installed.Files {
		if strings.HasPrefix(path, "parsers/") || strings.HasPrefix(path, "parsers"+string(filepath.Separator)) {
			hadParsers = true
			break
		}
	}
	if hadParsers {
		for _, p := range manifest.Parsers {
			rp := filepath.Join("parsers", p)
			addCopy(rp, filepath.Join(packDir, "parsers", p), filepath.Join(projectDir, "parsers", p))
		}
	} else if len(manifest.Parsers) > 0 {
		result.Skipped = append(result.Skipped, "parser files (not previously installed, use pack apply --enable-parsers)")
	}
	// copy parser.yaml if the pack previously owned parsers
	if hadParsers && len(manifest.Parsers) > 0 {
		parserYaml := filepath.Join(packDir, "parsers", "parser.yaml")
		if _, err := os.Stat(parserYaml); err == nil {
			rp := filepath.Join("parsers", "parser.yaml")
			addCopy(rp, parserYaml, filepath.Join(projectDir, "parsers", "parser.yaml"))
		}
	}

	for _, c := range copies {
		if err := safeCopyFile(c.src, c.dst, projectDir); err != nil {
			updateErr = err
			return nil, fmt.Errorf("updating %s: %w", c.relPath, updateErr)
		}
		h, _ := ComputeFileHash(c.dst)
		newFiles[c.relPath] = h
	}

	// record config.yaml hash if config/ontology was updated
	if configUpdated {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "config.yaml"))
		if h != "" {
			newFiles["config.yaml"] = h
		}
	}

	// compute desired file set from new manifest
	desiredPaths := make(map[string]bool)
	for _, p := range manifest.Prompts {
		desiredPaths[filepath.Join("prompts", p)] = true
	}
	for _, s := range manifest.Skills {
		desiredPaths[filepath.Join("skills", s)] = true
	}
	for _, s := range manifest.Samples {
		desiredPaths[filepath.Join("raw", s)] = true
	}
	for _, p := range manifest.Parsers {
		desiredPaths[filepath.Join("parsers", p)] = true
	}
	if len(manifest.Parsers) > 0 || parserYamlExists(packDir) {
		desiredPaths[filepath.Join("parsers", "parser.yaml")] = true
	}
	// config.yaml is always desired if previously owned — never delete as obsolete
	if _, ownsConfig := installed.Files["config.yaml"]; ownsConfig {
		desiredPaths["config.yaml"] = true
	}
	if configUpdated {
		desiredPaths["config.yaml"] = true
	}

	// delete obsolete files (in old version but not new), keep modified ones
	for path, hash := range installed.Files {
		if _, stillDesired := desiredPaths[path]; stillDesired {
			// still in new version — keep if not already updated
			if _, updated := newFiles[path]; !updated {
				newFiles[path] = hash
			}
			continue
		}
		// file removed from new version — restore snapshot or delete if unmodified
		fullPath := filepath.Join(projectDir, path)
		currentHash, _ := ComputeFileHash(fullPath)
		if currentHash == hash {
			// check if a pre-pack snapshot exists — restore instead of delete
			if installed.Snapshots != nil {
				if snapHash, ok := installed.Snapshots[path]; ok && snapHash != nil {
					snapPath := filepath.Join(projectDir, ".sage", "pack-snapshots", name, path)
					if data, err := os.ReadFile(snapPath); err == nil {
						os.WriteFile(fullPath, data, 0o644)
						result.Updated = append(result.Updated, path+" (restored to pre-pack)")
						continue
					}
				}
			}
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				updateErr = err
				return nil, fmt.Errorf("removing obsolete file %s: %w", path, updateErr)
			}
			result.Updated = append(result.Updated, path+" (removed)")
		} else {
			result.Conflicts = append(result.Conflicts, path+" (obsolete but modified)")
			newFiles[path] = hash
		}
	}

	// only advance version if all files were applied (no skips/conflicts)
	recordVersion := manifest.Version
	if len(result.Skipped) > 0 || len(result.Conflicts) > 0 {
		recordVersion = installed.Version // keep old version — update is partial
	}
	state.RecordInstall(name, recordVersion, installed.Source, newFiles)

	// persist state as part of the transaction
	if err := state.Save(projectDir); err != nil {
		updateErr = err
		return nil, fmt.Errorf("saving pack state: %w", updateErr)
	}

	if recordVersion == manifest.Version {
		result.Updated = append(result.Updated, fmt.Sprintf("%s@%s", name, manifest.Version))
	}
	return result, nil
}

func parserYamlExists(packDir string) bool {
	_, err := os.Stat(filepath.Join(packDir, "parsers", "parser.yaml"))
	return err == nil
}

