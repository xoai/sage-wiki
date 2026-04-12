package facts

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Aliases holds alias normalization configuration.
type Aliases struct {
	EntityAliases map[string][]string `yaml:"entity_aliases"` // canonical → []alias
	LabelAliases  map[string][]string `yaml:"label_aliases"`  // canonical → []alias
}

// ImportReport holds import statistics.
type ImportReport struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"` // skipped via upsert dedup
	Errors  int `json:"errors"`
	Files   int `json:"files"` // number of .numbers.yaml files processed
}

// numbersFile is the top-level structure of a .numbers.yaml file.
type numbersFile struct {
	Numbers []numberEntry `yaml:"numbers"`
}

// numberEntry represents a single numeric entry in a .numbers.yaml file.
type numberEntry struct {
	Value            string  `yaml:"value"`
	Numeric          float64 `yaml:"numeric"`
	Sign             string  `yaml:"sign"`
	NumberType       string  `yaml:"number_type"`
	Certainty        string  `yaml:"certainty"`
	Entity           string  `yaml:"entity"`
	EntityType       string  `yaml:"entity_type"`
	Period           string  `yaml:"period"`
	PeriodType       string  `yaml:"period_type"`
	SemanticLabel    string  `yaml:"semantic_label"`
	SourceFile       string  `yaml:"source_file"`
	SourceLocation   string  `yaml:"source_location"`
	ContextType      string  `yaml:"context_type"`
	ExactQuote       string  `yaml:"exact_quote"`
	Verified         bool    `yaml:"verified"`
	ExtractionMethod string  `yaml:"extraction_method"`
}

// extractMeta corresponds to extract-meta.yaml.
type extractMeta struct {
	SchemaVersion string `yaml:"schema_version"`
	Extractor     string `yaml:"extractor"`
}

// ImportDir imports all .numbers.yaml files from .pre-extracted/ into the facts table.
func ImportDir(store *Store, projectDir string, aliases *Aliases) (ImportReport, error) {
	var report ImportReport

	preDir := filepath.Join(projectDir, ".pre-extracted")

	// Check extract-meta.yaml version
	metaPath := filepath.Join(preDir, "extract-meta.yaml")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return report, fmt.Errorf("read extract-meta.yaml: %w", err)
	}

	var meta extractMeta
	if err := yaml.Unmarshal(metaData, &meta); err != nil {
		return report, fmt.Errorf("parse extract-meta.yaml: %w", err)
	}

	if !strings.HasPrefix(meta.SchemaVersion, "1.") {
		return report, fmt.Errorf("incompatible schema version: %s (expected 1.x)", meta.SchemaVersion)
	}

	// Build reverse alias lookups
	entityLookup := buildReverseLookup(aliases, true)
	labelLookup := buildReverseLookup(aliases, false)

	// Walk .numbers.yaml files
	filesDir := filepath.Join(preDir, "files")
	err = filepath.WalkDir(filesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable files
		}
		if d.IsDir() || !strings.HasSuffix(path, ".numbers.yaml") {
			return nil
		}

		report.Files++
		importErr := importOneFile(store, path, filesDir, meta.SchemaVersion, entityLookup, labelLookup, &report)
		if importErr != nil {
			report.Errors++
		}
		return nil
	})

	return report, err
}

func importOneFile(store *Store, path string, filesDir string, schemaVersion string, entityLookup, labelLookup map[string]string, report *ImportReport) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var nf numbersFile
	if err := yaml.Unmarshal(data, &nf); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	// Infer source_file relative path from the file path
	relPath, _ := filepath.Rel(filesDir, path)
	// e.g. inbox/report.pdf.numbers.yaml → inbox/report.pdf
	sourceRel := strings.TrimSuffix(relPath, ".numbers.yaml")

	return store.db.WriteTx(func(tx *sql.Tx) error {
		for _, n := range nf.Numbers {
			entity := normalizeAlias(n.Entity, entityLookup)
			label := normalizeAlias(n.SemanticLabel, labelLookup)

			qh := ""
			if n.ExactQuote != "" {
				h := sha256.Sum256([]byte(n.ExactQuote))
				qh = fmt.Sprintf("%x", h[:8])
			}

			sourceFile := "raw/" + sourceRel
			if n.SourceFile != "" {
				// If YAML has a source_file field, override with path-derived value
				// Keep raw/ prefix for manifest consistency
			}

			result, err := tx.Exec(`
				INSERT OR IGNORE INTO facts (
					source_file, value, numeric, sign,
					number_type, certainty, entity, entity_type,
					period, period_type, semantic_label,
					source_location, context_type, exact_quote,
					verified, extraction_method, schema_version, quote_hash
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sourceFile, n.Value, n.Numeric, n.Sign,
				n.NumberType, n.Certainty, entity, n.EntityType,
				n.Period, n.PeriodType, label,
				n.SourceLocation, n.ContextType, n.ExactQuote,
				n.Verified, n.ExtractionMethod, schemaVersion, qh,
			)
			if err != nil {
				return err
			}

			affected, _ := result.RowsAffected()
			if affected > 0 {
				report.Added++
			} else {
				report.Skipped++
			}
		}
		return nil
	})
}

// buildReverseLookup builds an alias → canonical reverse lookup map.
func buildReverseLookup(aliases *Aliases, isEntity bool) map[string]string {
	if aliases == nil {
		return nil
	}

	m := make(map[string]string)
	var source map[string][]string
	if isEntity {
		source = aliases.EntityAliases
	} else {
		source = aliases.LabelAliases
	}

	for canonical, aliasList := range source {
		for _, alias := range aliasList {
			m[alias] = canonical
		}
	}
	return m
}

// normalizeAlias looks up the alias map; returns canonical if found, original value otherwise.
func normalizeAlias(value string, lookup map[string]string) string {
	if lookup == nil || value == "" {
		return value
	}
	if canonical, ok := lookup[value]; ok {
		return canonical
	}
	return value
}
