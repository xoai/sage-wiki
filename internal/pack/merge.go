package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"gopkg.in/yaml.v3"
)

// ApplyMode controls how pack content is merged into a project.
type ApplyMode string

const (
	ModeReplace ApplyMode = "replace"
	ModeMerge   ApplyMode = "merge"
)

// ApplyResult summarizes what changed during pack apply.
type ApplyResult struct {
	ConfigChanges   []string
	OntologyAdded   []string
	PromptsAdded    []string
	PromptsConflict []string
	SkillsAdded     []string
	SkillsConflict  []string
	SamplesAdded    []string
	SamplesConflict []string
	ParsersAdded    []string
	ParsersConflict []string
	ParsersSkipped  bool // true if parser files were skipped (no opt-in)
}

// ApplyOpts provides optional flags for Apply.
type ApplyOpts struct {
	Source        string // "local", "registry", etc. — recorded in pack state
	EnableParsers bool   // opt-in to install parser files from this pack
}

// ValidateRelPath rejects paths that escape their intended directory.
func ValidateRelPath(relPath string) error {
	cleaned := filepath.Clean(relPath)
	if filepath.IsAbs(cleaned) {
		return fmt.Errorf("absolute path not allowed: %q", relPath)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path traversal not allowed: %q", relPath)
	}
	return nil
}

// validateManifestPaths checks all file paths in a manifest for traversal attacks.
func validateManifestPaths(m *PackManifest) error {
	for _, p := range m.Prompts {
		if err := ValidateRelPath(p); err != nil {
			return fmt.Errorf("prompt path: %w", err)
		}
	}
	for _, s := range m.Skills {
		if err := ValidateRelPath(s); err != nil {
			return fmt.Errorf("skill path: %w", err)
		}
	}
	for _, s := range m.Samples {
		if err := ValidateRelPath(s); err != nil {
			return fmt.Errorf("sample path: %w", err)
		}
	}
	for _, p := range m.Parsers {
		if err := ValidateRelPath(p); err != nil {
			return fmt.Errorf("parser path: %w", err)
		}
	}
	return nil
}

// Apply merges a pack into a project directory using the specified mode.
// The operation is transactional: on failure, pre-apply state is restored.
// Snapshots are preserved after apply so that `pack remove` can restore.
func Apply(projectDir, packDir string, manifest *PackManifest, mode ApplyMode, state *PackState, opts ...ApplyOpts) (*ApplyResult, error) {
	if err := validateManifestPaths(manifest); err != nil {
		return nil, fmt.Errorf("unsafe pack manifest: %w", err)
	}

	packName := manifest.Name

	// detect parser.yaml early (needed for both snapshot and apply decisions)
	packHasParserYaml := false
	if _, err := os.Stat(filepath.Join(packDir, "parsers", "parser.yaml")); err == nil {
		packHasParserYaml = true
	}

	// snapshot pre-apply state for rollback
	// only snapshot config.yaml if the pack actually modifies it
	var pathsToSnapshot []string
	hasConfig := manifest.Config != nil || len(manifest.ArticleFields) > 0
	hasOntology := len(manifest.Ontology.RelationTypes) > 0 || len(manifest.Ontology.EntityTypes) > 0
	if hasConfig || hasOntology {
		pathsToSnapshot = append(pathsToSnapshot, "config.yaml")
	}
	for _, p := range manifest.Prompts {
		pathsToSnapshot = append(pathsToSnapshot, filepath.Join("prompts", p))
	}
	for _, s := range manifest.Skills {
		pathsToSnapshot = append(pathsToSnapshot, filepath.Join("skills", s))
	}
	for _, s := range manifest.Samples {
		pathsToSnapshot = append(pathsToSnapshot, filepath.Join("raw", s))
	}
	// only snapshot parser paths when parsers will actually be installed
	enableParsers := len(opts) > 0 && opts[0].EnableParsers
	if enableParsers {
		for _, p := range manifest.Parsers {
			pathsToSnapshot = append(pathsToSnapshot, filepath.Join("parsers", p))
		}
		if len(manifest.Parsers) > 0 || packHasParserYaml {
			pathsToSnapshot = append(pathsToSnapshot, filepath.Join("parsers", "parser.yaml"))
		}
	}
	// if pack is already installed, use temp snapshot for rollback
	// to preserve the original first-apply snapshots for pack remove
	existing, _ := state.FindInstalled(packName)
	isReapply := existing != nil && existing.Snapshots != nil

	if isReapply {
		if err := SaveTempSnapshot(projectDir, packName, pathsToSnapshot); err != nil {
			return nil, fmt.Errorf("snapshot pre-reapply state: %w", err)
		}
		// extend permanent snapshots for newly owned paths (for remove)
		// only record snapshot hash after file is durably written
		snapshotDir := filepath.Join(projectDir, ".sage", "pack-snapshots", packName)
		if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
			return nil, fmt.Errorf("creating snapshot dir for reapply: %w", err)
		}
		for _, p := range pathsToSnapshot {
			if _, alreadySnapped := existing.Snapshots[p]; alreadySnapped {
				continue
			}
			src := filepath.Join(projectDir, p)
			info, err := os.Stat(src)
			if err != nil || info.IsDir() {
				existing.Snapshots[p] = nil
				continue
			}
			dst := filepath.Join(snapshotDir, p)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, fmt.Errorf("snapshot dir for %s: %w", p, err)
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return nil, fmt.Errorf("reading %s for snapshot: %w", p, err)
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return nil, fmt.Errorf("writing snapshot for %s: %w", p, err)
			}
			h, _ := ComputeFileHash(src)
			existing.Snapshots[p] = &h
		}
	} else {
		if err := state.SaveSnapshot(projectDir, packName, pathsToSnapshot); err != nil {
			return nil, fmt.Errorf("snapshot pre-apply state: %w", err)
		}
	}

	result := &ApplyResult{}
	var applyErr error

	defer func() {
		if applyErr != nil {
			if isReapply {
				_ = RestoreTempSnapshot(projectDir, packName, pathsToSnapshot)
			} else {
				_ = state.RestoreSnapshot(projectDir, packName)
			}
		} else if isReapply {
			_ = CleanTempSnapshot(projectDir, packName)
		}
	}()

	// merge config AND ontology in a single read-modify-write cycle
	if hasConfig || hasOntology {
		applyErr = applyConfigAndOntology(projectDir, manifest, mode, result)
		if applyErr != nil {
			return nil, fmt.Errorf("apply config/ontology: %w", applyErr)
		}
	}

	// copy prompts (mode-aware: merge skips existing, replace overwrites)
	if len(manifest.Prompts) > 0 {
		if mode == ModeReplace {
			for _, f := range manifest.Prompts {
				src := filepath.Join(packDir, "prompts", f)
				dst := filepath.Join(projectDir, "prompts", f)
				if err := safeCopyFile(src, dst, projectDir); err != nil {
					applyErr = err
					return nil, fmt.Errorf("apply prompt %s: %w", f, applyErr)
				}
				result.PromptsAdded = append(result.PromptsAdded, f)
			}
		} else {
			added, conflicts, err := MergePrompts(projectDir, packDir, manifest.Prompts)
			if err != nil {
				applyErr = err
				return nil, fmt.Errorf("apply prompts: %w", applyErr)
			}
			result.PromptsAdded = added
			result.PromptsConflict = conflicts
		}
	}

	// copy skills (conflict-aware in merge mode)
	for _, s := range manifest.Skills {
		dst := filepath.Join(projectDir, "skills", s)
		if mode == ModeMerge {
			if _, err := os.Stat(dst); err == nil {
				result.SkillsConflict = append(result.SkillsConflict, s)
				continue
			}
		}
		src := filepath.Join(packDir, "skills", s)
		if err := safeCopyFile(src, dst, projectDir); err != nil {
			applyErr = err
			return nil, fmt.Errorf("apply skill %s: %w", s, applyErr)
		}
		result.SkillsAdded = append(result.SkillsAdded, s)
	}

	// copy samples (conflict-aware in merge mode)
	for _, s := range manifest.Samples {
		dst := filepath.Join(projectDir, "raw", s)
		if mode == ModeMerge {
			if _, err := os.Stat(dst); err == nil {
				result.SamplesConflict = append(result.SamplesConflict, s)
				continue
			}
		}
		src := filepath.Join(packDir, "samples", s)
		if err := safeCopyFile(src, dst, projectDir); err != nil {
			applyErr = err
			return nil, fmt.Errorf("apply sample %s: %w", s, applyErr)
		}
		result.SamplesAdded = append(result.SamplesAdded, s)
	}

	// copy parsers (requires explicit opt-in via EnableParsers)
	hasParserContent := len(manifest.Parsers) > 0 || packHasParserYaml
	if hasParserContent && !enableParsers {
		result.ParsersSkipped = true
		fmt.Fprintf(os.Stderr, "Note: Pack %q includes parsers but they were not enabled. Use --enable-parsers to install parser files.\n", packName)
	}
	parserYamlWritten := false
	if hasParserContent && enableParsers {
		parserYaml := filepath.Join(packDir, "parsers", "parser.yaml")
		parserYamlDst := filepath.Join(projectDir, "parsers", "parser.yaml")
		parserYamlConflict := false

		// copy parser.yaml — required for parser scripts to be discoverable
		if _, err := os.Stat(parserYaml); err != nil {
			// pack has no parser.yaml — scripts would be undiscoverable, skip all
			fmt.Fprintf(os.Stderr, "Warning: Pack %q declares parsers but has no parsers/parser.yaml. Parser scripts skipped.\n", packName)
			parserYamlConflict = true
			for _, p := range manifest.Parsers {
				result.ParsersConflict = append(result.ParsersConflict, p+" (no parser.yaml in pack)")
			}
		} else if mode != ModeMerge {
			if err := safeCopyFile(parserYaml, parserYamlDst, projectDir); err != nil {
				applyErr = err
				return nil, fmt.Errorf("apply parser.yaml: %w", applyErr)
			}
			parserYamlWritten = true
		} else if _, err := os.Stat(parserYamlDst); err != nil {
			if err := safeCopyFile(parserYaml, parserYamlDst, projectDir); err != nil {
				applyErr = err
				return nil, fmt.Errorf("apply parser.yaml: %w", applyErr)
			}
			parserYamlWritten = true
		} else {
			parserYamlConflict = true
			result.ParsersConflict = append(result.ParsersConflict, "parser.yaml (existing)")
		}

		// in merge mode, if parser.yaml conflicted, skip all scripts too
		if parserYamlConflict {
			for _, p := range manifest.Parsers {
				result.ParsersConflict = append(result.ParsersConflict, p)
			}
		} else {
			for _, p := range manifest.Parsers {
				dst := filepath.Join(projectDir, "parsers", p)
				if mode == ModeMerge {
					if _, err := os.Stat(dst); err == nil {
						result.ParsersConflict = append(result.ParsersConflict, p)
						continue
					}
				}
				src := filepath.Join(packDir, "parsers", p)
				if err := safeCopyFile(src, dst, projectDir); err != nil {
					applyErr = err
					return nil, fmt.Errorf("apply parser %s: %w", p, applyErr)
				}
				result.ParsersAdded = append(result.ParsersAdded, p)
			}
		}
	}

	// on reapply, keep ownership hashes only for paths still in the new manifest
	// (skipped/conflicted files keep their hashes, stale files are dropped)
	files := make(map[string]string)
	if isReapply {
		newManifestPaths := make(map[string]bool)
		for _, p := range manifest.Prompts {
			newManifestPaths[filepath.Join("prompts", p)] = true
		}
		for _, s := range manifest.Skills {
			newManifestPaths[filepath.Join("skills", s)] = true
		}
		for _, s := range manifest.Samples {
			newManifestPaths[filepath.Join("raw", s)] = true
		}
		if enableParsers {
			for _, p := range manifest.Parsers {
				newManifestPaths[filepath.Join("parsers", p)] = true
			}
			if len(manifest.Parsers) > 0 || packHasParserYaml {
				newManifestPaths[filepath.Join("parsers", "parser.yaml")] = true
			}
		} else if existing != nil {
			for k := range existing.Files {
				if strings.HasPrefix(k, "parsers"+string(filepath.Separator)) || k == filepath.Join("parsers", "parser.yaml") {
					newManifestPaths[k] = true
				}
			}
		}
		if hasConfig || hasOntology {
			newManifestPaths["config.yaml"] = true
		}
		for k, v := range existing.Files {
			if newManifestPaths[k] {
				files[k] = v
			}
		}
	}
	for _, p := range result.PromptsAdded {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "prompts", p))
		files[filepath.Join("prompts", p)] = h
	}
	for _, s := range result.SkillsAdded {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "skills", s))
		files[filepath.Join("skills", s)] = h
	}
	for _, s := range result.SamplesAdded {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "raw", s))
		files[filepath.Join("raw", s)] = h
	}
	for _, p := range result.ParsersAdded {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "parsers", p))
		files[filepath.Join("parsers", p)] = h
	}
	// record parser.yaml ownership if it was actually written
	if parserYamlWritten || len(result.ParsersAdded) > 0 {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "parsers", "parser.yaml"))
		if h != "" {
			files[filepath.Join("parsers", "parser.yaml")] = h
		}
	}
	// record config.yaml hash if the pack modified it
	if hasConfig || hasOntology {
		h, _ := ComputeFileHash(filepath.Join(projectDir, "config.yaml"))
		if h != "" {
			files["config.yaml"] = h
		}
	}

	installSource := "local"
	if len(opts) > 0 && opts[0].Source != "" {
		installSource = opts[0].Source
	}
	state.RecordInstall(packName, manifest.Version, installSource, files)

	// persist state as part of the transaction — rollback if save fails
	if err := state.Save(projectDir); err != nil {
		applyErr = err
		return nil, fmt.Errorf("saving pack state: %w", applyErr)
	}

	return result, nil
}

// PackMerge performs fill-only merge: overlay values apply only when
// base has zero value. Maps merge recursively, scalars fill only if
// the base value is zero. User values always win.
func PackMerge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		existing, exists := out[k]
		if !exists {
			// key absent in base — fill from overlay
			out[k] = v
			continue
		}
		if overlayMap, ok := v.(map[string]any); ok {
			if baseMap, ok := existing.(map[string]any); ok {
				out[k] = PackMerge(baseMap, overlayMap)
				continue
			}
		}
		// key present in base — user value wins (even if false/0/"")
		// only fill when the value is literally nil (unset)
		if existing != nil {
			continue
		}
		out[k] = v
	}
	return out
}

// MergeOntology combines base and overlay ontology configs using union
// semantics: new types are added, existing types get their synonyms merged,
// nothing is removed.
func MergeOntology(base, overlay config.OntologyConfig) config.OntologyConfig {
	// normalize: merge Relations and RelationTypes into one slice
	baseRelations := normalizeRelations(base)
	overlayRelations := normalizeRelations(overlay)

	return config.OntologyConfig{
		RelationTypes: mergeRelations(baseRelations, overlayRelations),
		EntityTypes:   mergeEntityTypes(base.EntityTypes, overlay.EntityTypes),
	}
}

func normalizeRelations(o config.OntologyConfig) []config.RelationConfig {
	combined := make([]config.RelationConfig, 0, len(o.Relations)+len(o.RelationTypes))
	seen := make(map[string]bool)
	for _, r := range o.RelationTypes {
		combined = append(combined, r)
		seen[r.Name] = true
	}
	for _, r := range o.Relations {
		if !seen[r.Name] {
			combined = append(combined, r)
			seen[r.Name] = true
		}
	}
	return combined
}

func mergeRelations(base, overlay []config.RelationConfig) []config.RelationConfig {
	byName := make(map[string]int, len(base))
	result := make([]config.RelationConfig, len(base))
	copy(result, base)
	for i, r := range result {
		byName[r.Name] = i
	}
	for _, r := range overlay {
		if idx, exists := byName[r.Name]; exists {
			seen := make(map[string]bool)
			for _, s := range result[idx].Synonyms {
				seen[s] = true
			}
			for _, s := range r.Synonyms {
				if !seen[s] {
					result[idx].Synonyms = append(result[idx].Synonyms, s)
				}
			}
		} else {
			byName[r.Name] = len(result)
			result = append(result, r)
		}
	}
	return result
}

func mergeEntityTypes(base, overlay []config.EntityTypeConfig) []config.EntityTypeConfig {
	seen := make(map[string]bool, len(base))
	result := make([]config.EntityTypeConfig, len(base))
	copy(result, base)
	for _, e := range result {
		seen[e.Name] = true
	}
	for _, e := range overlay {
		if !seen[e.Name] {
			result = append(result, e)
			seen[e.Name] = true
		}
	}
	return result
}

// MergePrompts copies prompt files from packDir to projectDir.
// New files are copied; existing files are flagged as conflicts.
func MergePrompts(projectDir, packDir string, files []string) (added, conflicts []string, err error) {
	promptsDir := filepath.Join(projectDir, "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		src := filepath.Join(packDir, "prompts", f)
		dst := filepath.Join(promptsDir, f)
		if _, err := os.Stat(dst); err == nil {
			conflicts = append(conflicts, f)
			continue
		}
		if err := safeCopyFile(src, dst, projectDir); err != nil {
			return added, conflicts, fmt.Errorf("copy prompt %s: %w", f, err)
		}
		added = append(added, f)
	}
	return added, conflicts, nil
}

// configAllowlist contains top-level config keys packs are allowed to set.
// Keys not on this list are stripped to prevent credential/endpoint hijacking.
var configAllowlist = map[string]bool{
	"compiler":     true,
	"search":       true,
	"linting":      true,
	"ontology":     true,
	"trust":        true,
	"type_signals": true,
	"ignore":       true,
}

// stripDenylisted removes config keys not on the allowlist.
func stripDenylisted(overlay map[string]any) (map[string]any, []string) {
	cleaned := make(map[string]any, len(overlay))
	var stripped []string
	for k, v := range overlay {
		if !configAllowlist[k] {
			stripped = append(stripped, k)
			continue
		}
		cleaned[k] = v
	}
	return cleaned, stripped
}

// applyConfigAndOntology merges config overlay and ontology in a single
// read-modify-write cycle to avoid data loss from separate writes.
func applyConfigAndOntology(projectDir string, manifest *PackManifest, mode ApplyMode, result *ApplyResult) error {
	configPath := filepath.Join(projectDir, "config.yaml")

	var base map[string]any
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			base = make(map[string]any)
		} else {
			return fmt.Errorf("reading config: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(data, &base); err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}
	}

	// merge config overlay (with security-sensitive keys stripped)
	if manifest.Config != nil {
		safe, stripped := stripDenylisted(manifest.Config)
		if len(stripped) > 0 {
			fmt.Fprintf(os.Stderr, "Warning: Pack %q tried to set restricted config keys (ignored): %s\n",
				manifest.Name, strings.Join(stripped, ", "))
		}
		if len(safe) > 0 {
			switch mode {
			case ModeReplace:
				base = replacemerge(base, safe)
			default:
				base = PackMerge(base, safe)
			}
			for k := range safe {
				result.ConfigChanges = append(result.ConfigChanges, k)
			}
		}
	}

	// merge article_fields into compiler section
	if len(manifest.ArticleFields) > 0 {
		compiler, _ := base["compiler"].(map[string]any)
		if compiler == nil {
			compiler = make(map[string]any)
		}
		if _, exists := compiler["article_fields"]; !exists || mode == ModeReplace {
			compiler["article_fields"] = manifest.ArticleFields
			base["compiler"] = compiler
			result.ConfigChanges = append(result.ConfigChanges, "compiler.article_fields")
		}
	}

	// merge ontology into the same map
	if len(manifest.Ontology.RelationTypes) > 0 || len(manifest.Ontology.EntityTypes) > 0 {
		base = mergeOntologyIntoMap(base, manifest.Ontology)
		for _, r := range manifest.Ontology.RelationTypes {
			result.OntologyAdded = append(result.OntologyAdded, "relation:"+r.Name)
		}
		for _, e := range manifest.Ontology.EntityTypes {
			result.OntologyAdded = append(result.OntologyAdded, "entity:"+e.Name)
		}
	}

	out, err := yaml.Marshal(base)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	// validate the merged result before writing
	var merged config.Config
	if err := yaml.Unmarshal(out, &merged); err != nil {
		return fmt.Errorf("merged config is invalid YAML: %w", err)
	}
	// set required fields that packs don't provide (project, sources, output)
	// so validation focuses on the pack's contributions
	if merged.Project == "" {
		merged.Project = "pack-validation"
	}
	if len(merged.Sources) == 0 {
		merged.Sources = []config.Source{{Path: "raw", Type: "auto"}}
	}
	if merged.Output == "" {
		merged.Output = "wiki"
	}
	if err := merged.Validate(); err != nil {
		return fmt.Errorf("merged config fails validation: %w", err)
	}

	return os.WriteFile(configPath, out, 0o644)
}

// mergeOntologyIntoMap merges ontology overlay into the raw config map,
// preserving all other map keys intact.
func mergeOntologyIntoMap(base map[string]any, overlay OntologyOverlay) map[string]any {
	// extract current ontology from map
	var baseOntology config.OntologyConfig
	if ontRaw, ok := base["ontology"]; ok {
		if b, err := yaml.Marshal(ontRaw); err == nil {
			yaml.Unmarshal(b, &baseOntology)
		}
	}

	merged := MergeOntology(baseOntology, config.OntologyConfig{
		RelationTypes: overlay.RelationTypes,
		EntityTypes:   overlay.EntityTypes,
	})

	// convert back to map[string]any for unified marshal
	var ontMap map[string]any
	if b, err := yaml.Marshal(merged); err == nil {
		yaml.Unmarshal(b, &ontMap)
	}
	base["ontology"] = ontMap

	return base
}

// replacemerge does a deepMerge where overlay values replace base values.
func replacemerge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if overlayMap, ok := v.(map[string]any); ok {
			if baseMap, ok := out[k].(map[string]any); ok {
				out[k] = replacemerge(baseMap, overlayMap)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// safeCopyFile copies src to dst, verifying the resolved destination
// stays within allowedRoot to prevent symlink-based escapes.
func safeCopyFile(src, dst, allowedRoot string) error {
	absRoot, _ := filepath.Abs(allowedRoot)
	absRoot, _ = filepath.EvalSymlinks(absRoot)

	// validate existing parent chain before creating directories
	dir := filepath.Dir(dst)
	existing := dir
	for existing != "" && existing != "." && existing != "/" {
		if info, err := os.Lstat(existing); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err := filepath.EvalSymlinks(existing)
				if err != nil {
					return fmt.Errorf("resolving symlink at %q: %w", existing, err)
				}
				if !strings.HasPrefix(resolved, absRoot+string(filepath.Separator)) && resolved != absRoot {
					return fmt.Errorf("parent %q is a symlink resolving outside project (%q)", existing, resolved)
				}
			}
			break
		}
		existing = filepath.Dir(existing)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolving destination dir: %w", err)
	}
	if !strings.HasPrefix(realDir, absRoot+string(filepath.Separator)) && realDir != absRoot {
		return fmt.Errorf("destination %q resolves outside project (%q)", dst, realDir)
	}
	// reject symlink leaf
	realDst := filepath.Join(realDir, filepath.Base(dst))
	if info, err := os.Lstat(realDst); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination %q is a symlink", dst)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := srcInfo.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(realDst, data, mode)
}
