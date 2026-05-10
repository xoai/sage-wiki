package pack

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CreateScaffold generates a pack directory structure with templates.
func CreateScaffold(name, dir string) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	packDir := filepath.Join(dir, name)
	if _, err := os.Stat(packDir); err == nil {
		return fmt.Errorf("directory %q already exists", packDir)
	}

	dirs := []string{
		packDir,
		filepath.Join(packDir, "prompts"),
		filepath.Join(packDir, "skills"),
		filepath.Join(packDir, "samples"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}

	manifest := PackManifest{
		Name:        name,
		Version:     "0.1.0",
		Description: "A sage-wiki contribution pack",
		Author:      "your-name",
		Tags:        []string{},
		Prompts:     []string{"example-prompt.md"},
		Samples:     []string{"example-source.md"},
	}

	data, err := yaml.Marshal(&manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), data, 0o644); err != nil {
		return err
	}

	// example prompt
	promptContent := `---
name: example-prompt
description: Example prompt template for ` + name + `
---

You are a research assistant. Summarize the following content:

{{.Content}}
`
	if err := os.WriteFile(filepath.Join(packDir, "prompts", "example-prompt.md"), []byte(promptContent), 0o644); err != nil {
		return err
	}

	// example sample source
	sampleContent := `---
title: Example Source
type: article
---

# Example Source

This is a sample source file for the ` + name + ` pack.
Replace this with real examples.
`
	if err := os.WriteFile(filepath.Join(packDir, "samples", "example-source.md"), []byte(sampleContent), 0o644); err != nil {
		return err
	}

	// README
	readme := `# ` + name + `

A sage-wiki contribution pack.

## Installation

` + "```" + `
sage-wiki pack install .
sage-wiki pack apply ` + name + `
` + "```" + `

## Contents

- **prompts/** — Custom prompt templates
- **samples/** — Example source files
- **skills/** — Agent skill templates

## License

MIT
`
	if err := os.WriteFile(filepath.Join(packDir, "README.md"), []byte(readme), 0o644); err != nil {
		return err
	}

	return nil
}

// CreateScaffoldFromProject generates a pack pre-filled from the project's config.
func CreateScaffoldFromProject(name, dir, projectDir string) error {
	if err := CreateScaffold(name, dir); err != nil {
		return err
	}

	// try to read project config and pre-fill ontology
	configPath := filepath.Join(projectDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil // no config — scaffold is still valid
	}

	var projectConfig struct {
		Ontology struct {
			RelationTypes []struct {
				Name     string   `yaml:"name"`
				Synonyms []string `yaml:"synonyms"`
			} `yaml:"relation_types"`
			EntityTypes []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			} `yaml:"entity_types"`
		} `yaml:"ontology"`
	}
	if err := yaml.Unmarshal(data, &projectConfig); err != nil {
		return nil
	}

	if len(projectConfig.Ontology.RelationTypes) == 0 && len(projectConfig.Ontology.EntityTypes) == 0 {
		return nil
	}

	// update pack.yaml with project ontology
	packYamlPath := filepath.Join(dir, name, "pack.yaml")
	packData, err := os.ReadFile(packYamlPath)
	if err != nil {
		return nil
	}

	var manifest map[string]any
	yaml.Unmarshal(packData, &manifest)
	manifest["ontology"] = projectConfig.Ontology

	out, _ := yaml.Marshal(manifest)
	os.WriteFile(packYamlPath, out, 0o644)

	return nil
}
