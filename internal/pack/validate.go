package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"gopkg.in/yaml.v3"
)

// ValidationError represents an issue found during pack validation.
type ValidationError struct {
	Level   string // "error" or "warning"
	Field   string
	Message string
}

func (e ValidationError) String() string {
	return fmt.Sprintf("[%s] %s: %s", e.Level, e.Field, e.Message)
}

// Validate runs all validation checks on a pack directory.
func Validate(dir string) ([]ValidationError, error) {
	var errors []ValidationError

	// 1. Schema: required fields
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("loading manifest: %w", err)
	}

	// 2. Name format (already checked by LoadManifest, but re-verify)
	if err := ValidateName(manifest.Name); err != nil {
		errors = append(errors, ValidationError{"error", "name", err.Error()})
	}

	// 3. Version format
	if err := ValidateVersion(manifest.Version); err != nil {
		errors = append(errors, ValidationError{"error", "version", err.Error()})
	}

	// 4. MinVersion valid
	if manifest.MinVersion != "" {
		if err := ValidateVersion(manifest.MinVersion); err != nil {
			errors = append(errors, ValidationError{"error", "min_version", err.Error()})
		}
	}

	// 5. Listed files exist
	for _, p := range manifest.Prompts {
		path := filepath.Join(dir, "prompts", p)
		if _, err := os.Stat(path); err != nil {
			errors = append(errors, ValidationError{"error", "prompts", fmt.Sprintf("file not found: %s", p)})
		}
	}
	for _, s := range manifest.Skills {
		path := filepath.Join(dir, "skills", s)
		if _, err := os.Stat(path); err != nil {
			errors = append(errors, ValidationError{"error", "skills", fmt.Sprintf("file not found: %s", s)})
		}
	}
	for _, s := range manifest.Samples {
		path := filepath.Join(dir, "samples", s)
		if _, err := os.Stat(path); err != nil {
			errors = append(errors, ValidationError{"error", "samples", fmt.Sprintf("file not found: %s", s)})
		}
	}
	for _, p := range manifest.Parsers {
		path := filepath.Join(dir, "parsers", p)
		if _, err := os.Stat(path); err != nil {
			errors = append(errors, ValidationError{"error", "parsers", fmt.Sprintf("file not found: %s", p)})
		}
	}

	// 6. Path containment
	allPaths := make([]string, 0)
	allPaths = append(allPaths, manifest.Prompts...)
	allPaths = append(allPaths, manifest.Skills...)
	allPaths = append(allPaths, manifest.Samples...)
	allPaths = append(allPaths, manifest.Parsers...)
	for _, p := range allPaths {
		if err := ValidateRelPath(p); err != nil {
			errors = append(errors, ValidationError{"error", "paths", fmt.Sprintf("%s: %v", p, err)})
		}
	}

	// symlink check
	absDir, _ := filepath.Abs(dir)
	for _, p := range manifest.Prompts {
		checkSymlink(absDir, filepath.Join(dir, "prompts", p), &errors)
	}
	for _, s := range manifest.Skills {
		checkSymlink(absDir, filepath.Join(dir, "skills", s), &errors)
	}
	for _, s := range manifest.Samples {
		checkSymlink(absDir, filepath.Join(dir, "samples", s), &errors)
	}

	// 7. Config overlay validates when merged with defaults
	if manifest.Config != nil {
		if errs := validateConfigOverlay(manifest.Config); len(errs) > 0 {
			for _, e := range errs {
				errors = append(errors, ValidationError{"error", "config", e})
			}
		}
	}

	// 8. Ontology type names valid
	for _, r := range manifest.Ontology.RelationTypes {
		if err := validateOntologyName(r.Name); err != nil {
			errors = append(errors, ValidationError{"error", "ontology.relation_types", err.Error()})
		}
	}
	for _, e := range manifest.Ontology.EntityTypes {
		if err := validateOntologyName(e.Name); err != nil {
			errors = append(errors, ValidationError{"error", "ontology.entity_types", err.Error()})
		}
	}

	// 9. Prompt files are valid markdown
	for _, p := range manifest.Prompts {
		path := filepath.Join(dir, "prompts", p)
		if !strings.HasSuffix(p, ".md") {
			errors = append(errors, ValidationError{"warning", "prompts", fmt.Sprintf("%s is not a .md file", p)})
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) == 0 {
			errors = append(errors, ValidationError{"warning", "prompts", fmt.Sprintf("%s is empty", p)})
		}
	}

	// 10. Destination safety
	for _, p := range manifest.Prompts {
		if !isAllowedDest("prompts", p) {
			errors = append(errors, ValidationError{"error", "prompts", fmt.Sprintf("%s resolves outside prompts/", p)})
		}
	}
	for _, s := range manifest.Skills {
		if !isAllowedDest("skills", s) {
			errors = append(errors, ValidationError{"error", "skills", fmt.Sprintf("%s resolves outside skills/", s)})
		}
	}
	for _, s := range manifest.Samples {
		if !isAllowedDest("raw", s) {
			errors = append(errors, ValidationError{"error", "samples", fmt.Sprintf("%s resolves outside raw/", s)})
		}
	}
	for _, p := range manifest.Parsers {
		if !isAllowedDest("parsers", p) {
			errors = append(errors, ValidationError{"error", "parsers", fmt.Sprintf("%s resolves outside parsers/", p)})
		}
	}

	return errors, nil
}

func checkSymlink(rootDir, path string, errors *[]ValidationError) {
	info, err := os.Lstat(path)
	if err != nil {
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			*errors = append(*errors, ValidationError{"error", "paths", fmt.Sprintf("cannot resolve symlink: %s", path)})
			return
		}
		if !strings.HasPrefix(resolved, rootDir+string(filepath.Separator)) && resolved != rootDir {
			*errors = append(*errors, ValidationError{"error", "paths", fmt.Sprintf("symlink escapes pack directory: %s → %s", path, resolved)})
		}
	}
}

func validateConfigOverlay(overlay map[string]any) []string {
	defaults := config.Defaults()
	data, err := yaml.Marshal(defaults)
	if err != nil {
		return nil
	}
	var base map[string]any
	yaml.Unmarshal(data, &base)

	// use replace semantics so every overlay value is exercised during validation
	// (fill-only would hide invalid values behind defaults)
	safe, _ := stripDenylisted(overlay)
	merged := replacemerge(base, safe)
	mergedData, _ := yaml.Marshal(merged)

	var cfg config.Config
	if err := yaml.Unmarshal(mergedData, &cfg); err != nil {
		return []string{fmt.Sprintf("config overlay produces invalid config: %v", err)}
	}

	// set required fields that the overlay shouldn't need to provide
	cfg.Project = "validation-test"
	cfg.Sources = []config.Source{{Path: "raw", Type: "auto"}}
	if cfg.Output == "" {
		cfg.Output = "wiki"
	}

	if err := cfg.Validate(); err != nil {
		return []string{fmt.Sprintf("merged config fails validation: %v", err)}
	}
	return nil
}

func validateOntologyName(name string) error {
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
			return fmt.Errorf("invalid ontology name %q: must be lowercase with underscores", name)
		}
	}
	if len(name) == 0 {
		return fmt.Errorf("ontology name is empty")
	}
	if name[0] < 'a' || name[0] > 'z' {
		return fmt.Errorf("ontology name %q must start with a letter", name)
	}
	return nil
}

func isAllowedDest(subdir, relPath string) bool {
	if filepath.IsAbs(relPath) {
		return false
	}
	cleaned := filepath.Clean(relPath)
	full := filepath.Join(subdir, cleaned)
	return strings.HasPrefix(full, subdir+string(filepath.Separator)) || full == subdir
}
