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

// Aliases 规范化别名配置。
type Aliases struct {
	EntityAliases map[string][]string `yaml:"entity_aliases"` // canonical → []alias
	LabelAliases  map[string][]string `yaml:"label_aliases"`  // canonical → []alias
}

// ImportReport 导入统计。
type ImportReport struct {
	Added   int `json:"added"`
	Skipped int `json:"skipped"` // upsert 去重跳过
	Errors  int `json:"errors"`
	Files   int `json:"files"` // 处理的 .numbers.yaml 文件数
}

// numbersFile 对应 .numbers.yaml 的顶层结构。
type numbersFile struct {
	Numbers []numberEntry `yaml:"numbers"`
}

// numberEntry 对应 .numbers.yaml 中的单条数字。
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

// extractMeta 对应 extract-meta.yaml。
type extractMeta struct {
	SchemaVersion string `yaml:"schema_version"`
	Extractor     string `yaml:"extractor"`
}

// ImportDir 从 .pre-extracted/ 导入所有 .numbers.yaml 到 facts 表。
func ImportDir(store *Store, projectDir string, aliases *Aliases) (ImportReport, error) {
	var report ImportReport

	preDir := filepath.Join(projectDir, ".pre-extracted")

	// 检查 extract-meta.yaml 版本
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

	// 构建 alias 反向映射
	entityLookup := buildReverseLookup(aliases, true)
	labelLookup := buildReverseLookup(aliases, false)

	// 扫描 .numbers.yaml 文件
	filesDir := filepath.Join(preDir, "files")
	err = filepath.WalkDir(filesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可读文件
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

	// 从路径推断 source_file 相对路径
	relPath, _ := filepath.Rel(filesDir, path)
	// inbox/投资建议书.pdf.numbers.yaml → inbox/投资建议书.pdf
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
				// 如果 YAML 中有 source_file 字段，用路径推导覆盖
				// 保持 raw/ 前缀用于 manifest 一致性
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

// buildReverseLookup 构建 alias → canonical 的反向映射。
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

// normalizeAlias 查别名映射，命中返回 canonical，未命中返回原值。
func normalizeAlias(value string, lookup map[string]string) string {
	if lookup == nil || value == "" {
		return value
	}
	if canonical, ok := lookup[value]; ok {
		return canonical
	}
	return value
}
