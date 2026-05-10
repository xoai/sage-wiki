package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"gopkg.in/yaml.v3"
)

// Version is the current sage-wiki version, set at build time via ldflags.
// When unset (running from source), CheckMinVersion always passes.
var Version = "dev"

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// PackManifest represents a pack.yaml file.
type PackManifest struct {
	Name          string            `yaml:"name"`
	Version       string            `yaml:"version"`
	Description   string            `yaml:"description"`
	Author        string            `yaml:"author"`
	License       string            `yaml:"license,omitempty"`
	MinVersion    string            `yaml:"min_version,omitempty"`
	Tags          []string          `yaml:"tags,omitempty"`
	Homepage      string            `yaml:"homepage,omitempty"`
	Config        map[string]any    `yaml:"config,omitempty"`
	Ontology      OntologyOverlay   `yaml:"ontology,omitempty"`
	ArticleFields []string          `yaml:"article_fields,omitempty"`
	Prompts       []string          `yaml:"prompts,omitempty"`
	Skills        []string          `yaml:"skills,omitempty"`
	Parsers       []string          `yaml:"parsers,omitempty"`
	Samples       []string          `yaml:"samples,omitempty"`
}

// OntologyOverlay defines ontology extensions provided by a pack.
type OntologyOverlay struct {
	RelationTypes []config.RelationConfig   `yaml:"relation_types,omitempty"`
	EntityTypes   []config.EntityTypeConfig `yaml:"entity_types,omitempty"`
}

// LoadManifest reads and parses pack.yaml from a pack directory.
func LoadManifest(dir string) (*PackManifest, error) {
	path := filepath.Join(dir, "pack.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading pack manifest: %w", err)
	}

	var m PackManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing pack.yaml: %w", err)
	}

	if err := m.validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

func (m *PackManifest) validate() error {
	if m.Name == "" {
		return fmt.Errorf("pack manifest: name is required")
	}
	if err := ValidateName(m.Name); err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}
	if m.Version == "" {
		return fmt.Errorf("pack manifest: version is required")
	}
	if err := ValidateVersion(m.Version); err != nil {
		return fmt.Errorf("pack manifest: %w", err)
	}
	if m.Description == "" {
		return fmt.Errorf("pack manifest: description is required")
	}
	if m.Author == "" {
		return fmt.Errorf("pack manifest: author is required")
	}
	if m.MinVersion != "" {
		if err := ValidateVersion(m.MinVersion); err != nil {
			return fmt.Errorf("pack manifest: invalid min_version: %w", err)
		}
	}
	return nil
}

// ValidateName checks that a pack name is kebab-case.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid pack name %q: must be kebab-case (a-z, 0-9, hyphens, starting with a letter)", name)
	}
	return nil
}

// ValidateVersion checks that a version string is valid semver (major.minor.patch).
func ValidateVersion(v string) error {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid version %q: must be semver (major.minor.patch)", v)
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return fmt.Errorf("invalid version %q: non-numeric component %q", v, p)
		}
	}
	return nil
}

// CheckMinVersion verifies the current sage-wiki version meets the pack's
// minimum version requirement. Always passes when Version is "dev".
func CheckMinVersion(minVer string) error {
	if Version == "dev" || minVer == "" {
		return nil
	}
	if err := ValidateVersion(minVer); err != nil {
		return err
	}
	if err := ValidateVersion(Version); err != nil {
		return nil // can't compare non-semver build versions — allow
	}
	if compareSemver(Version, minVer) < 0 {
		return fmt.Errorf("sage-wiki %s is older than required %s", Version, minVer)
	}
	return nil
}

// compareSemver returns -1, 0, or 1. Returns 0 if either string is not valid semver.
func compareSemver(a, b string) int {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	if len(ap) != 3 || len(bp) != 3 {
		return 0
	}
	for i := 0; i < 3; i++ {
		av, aerr := strconv.Atoi(ap[i])
		bv, berr := strconv.Atoi(bp[i])
		if aerr != nil || berr != nil {
			return 0
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}
