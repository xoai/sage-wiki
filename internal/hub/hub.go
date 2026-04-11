package hub

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Project represents a registered sage-wiki project.
type Project struct {
	Path        string `yaml:"path" json:"path"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Searchable  bool   `yaml:"searchable" json:"searchable"`
}

// HubConfig holds the multi-project registry.
type HubConfig struct {
	Projects map[string]Project `yaml:"projects" json:"projects"`
}

func New() *HubConfig {
	return &HubConfig{Projects: make(map[string]Project)}
}

func Load(path string) (*HubConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hub.Load: %w", err)
	}
	var cfg HubConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("hub.Load: %w", err)
	}
	if cfg.Projects == nil {
		cfg.Projects = make(map[string]Project)
	}
	return &cfg, nil
}

func (c *HubConfig) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("hub.Save: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// AddProject adds or updates a project. Returns true if it overwrote an existing entry.
func (c *HubConfig) AddProject(name string, p Project) bool {
	_, existed := c.Projects[name]
	c.Projects[name] = p
	return existed
}

func (c *HubConfig) RemoveProject(name string) {
	delete(c.Projects, name)
}

func (c *HubConfig) SearchableProjects() map[string]Project {
	result := make(map[string]Project)
	for name, p := range c.Projects {
		if p.Searchable {
			result[name] = p
		}
	}
	return result
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".sage-hub.yaml")
}
