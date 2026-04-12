package facts

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"

	"github.com/xoai/sage-wiki/internal/storage"
)

// Fact 表示一条结构化数字事实。
type Fact struct {
	ID               int64   `json:"id,omitempty"`
	SourceFile       string  `json:"source_file"`
	SourceProject    string  `json:"source_project,omitempty"`
	Value            string  `json:"value"`
	Numeric          float64 `json:"numeric"`
	Sign             string  `json:"sign,omitempty"`
	NumberType       string  `json:"number_type,omitempty"`
	Certainty        string  `json:"certainty,omitempty"`
	Entity           string  `json:"entity"`
	EntityType       string  `json:"entity_type,omitempty"`
	Period           string  `json:"period,omitempty"`
	PeriodType       string  `json:"period_type,omitempty"`
	SemanticLabel    string  `json:"semantic_label,omitempty"`
	SourceLocation   string  `json:"source_location,omitempty"`
	ContextType      string  `json:"context_type,omitempty"`
	ExactQuote       string  `json:"exact_quote,omitempty"`
	Verified         bool    `json:"verified,omitempty"`
	ExtractionMethod string  `json:"extraction_method,omitempty"`
	SchemaVersion    string  `json:"schema_version,omitempty"`
	ImportedAt       string  `json:"imported_at,omitempty"`
	QuoteHash        string  `json:"quote_hash,omitempty"`
}

// QueryOpts 查询条件。
type QueryOpts struct {
	Entity     string
	EntityType string
	Period     string
	Label      string
	NumberType string
	Source     string
	Limit      int
	Fuzzy      bool // 模糊匹配 entity 和 label（LIKE %keyword%）
}

// FactStats 汇总统计。
type FactStats struct {
	TotalFacts     int `json:"total_facts"`
	UniqueEntities int `json:"unique_entities"`
	UniquePeriods  int `json:"unique_periods"`
	UniqueLabels   int `json:"unique_labels"`
	UniqueSources  int `json:"unique_sources"`
}

// Store 管理 facts 表的读写。
type Store struct {
	db *storage.DB
}

// NewStore 创建 facts store。
func NewStore(db *storage.DB) *Store {
	return &Store{db: db}
}

// quoteHash 计算 exact_quote 的短哈希。
func quoteHash(quote string) string {
	if quote == "" {
		return ""
	}
	h := sha256.Sum256([]byte(quote))
	return fmt.Sprintf("%x", h[:8]) // 前 16 hex chars
}

// Insert 插入一条 fact（upsert：相同去重键时忽略）。
func (s *Store) Insert(f Fact) error {
	f.QuoteHash = quoteHash(f.ExactQuote)
	if f.Sign == "" {
		f.Sign = "positive"
	}
	if f.Certainty == "" {
		f.Certainty = "exact"
	}
	if f.Entity == "" {
		f.Entity = "unknown"
	}
	if f.Period == "" {
		f.Period = "unknown"
	}
	if f.PeriodType == "" {
		f.PeriodType = "unknown"
	}
	if f.SourceProject == "" {
		f.SourceProject = "local"
	}

	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO facts (
				source_file, source_project, value, numeric, sign,
				number_type, certainty, entity, entity_type,
				period, period_type, semantic_label,
				source_location, context_type, exact_quote,
				verified, extraction_method, schema_version, quote_hash
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.SourceFile, f.SourceProject, f.Value, f.Numeric, f.Sign,
			f.NumberType, f.Certainty, f.Entity, f.EntityType,
			f.Period, f.PeriodType, f.SemanticLabel,
			f.SourceLocation, f.ContextType, f.ExactQuote,
			f.Verified, f.ExtractionMethod, f.SchemaVersion, f.QuoteHash,
		)
		return err
	})
}

// Query 按条件查询 facts。
func (s *Store) Query(opts QueryOpts) ([]Fact, error) {
	var where []string
	var args []interface{}

	if opts.Entity != "" {
		if opts.Fuzzy {
			where = append(where, "entity LIKE ?")
			args = append(args, "%"+opts.Entity+"%")
		} else {
			where = append(where, "entity = ?")
			args = append(args, opts.Entity)
		}
	}
	if opts.EntityType != "" {
		where = append(where, "entity_type = ?")
		args = append(args, opts.EntityType)
	}
	if opts.Period != "" {
		where = append(where, "period = ?")
		args = append(args, opts.Period)
	}
	if opts.Label != "" {
		if opts.Fuzzy {
			where = append(where, "semantic_label LIKE ?")
			args = append(args, "%"+opts.Label+"%")
		} else {
			where = append(where, "semantic_label = ?")
			args = append(args, opts.Label)
		}
	}
	if opts.NumberType != "" {
		where = append(where, "number_type = ?")
		args = append(args, opts.NumberType)
	}
	if opts.Source != "" {
		where = append(where, "source_file = ?")
		args = append(args, opts.Source)
	}

	q := "SELECT id, source_file, source_project, value, numeric, sign, number_type, certainty, entity, entity_type, period, period_type, semantic_label, source_location, context_type, exact_quote, verified, extraction_method, schema_version, imported_at, quote_hash FROM facts"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY entity, period, semantic_label"

	limit := opts.Limit
	if limit <= 0 {
		limit = 1000
	}
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.ReadDB().Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("facts.Query: %w", err)
	}
	defer rows.Close()

	var results []Fact
	for rows.Next() {
		var f Fact
		var importedAt, quoteHash sql.NullString
		err := rows.Scan(
			&f.ID, &f.SourceFile, &f.SourceProject, &f.Value, &f.Numeric,
			&f.Sign, &f.NumberType, &f.Certainty, &f.Entity, &f.EntityType,
			&f.Period, &f.PeriodType, &f.SemanticLabel,
			&f.SourceLocation, &f.ContextType, &f.ExactQuote,
			&f.Verified, &f.ExtractionMethod, &f.SchemaVersion,
			&importedAt, &quoteHash,
		)
		if err != nil {
			return nil, fmt.Errorf("facts.Query scan: %w", err)
		}
		f.ImportedAt = importedAt.String
		f.QuoteHash = quoteHash.String
		results = append(results, f)
	}
	return results, rows.Err()
}

// DeleteBySource 删除指定源文件的所有 facts。
func (s *Store) DeleteBySource(sourceFile string) (int64, error) {
	var affected int64
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		result, err := tx.Exec("DELETE FROM facts WHERE source_file = ?", sourceFile)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

// Stats 返回 facts 表的汇总统计。
func (s *Store) Stats() (FactStats, error) {
	var stats FactStats
	row := s.db.ReadDB().QueryRow(`
		SELECT
			COUNT(*),
			COUNT(DISTINCT entity),
			COUNT(DISTINCT period),
			COUNT(DISTINCT semantic_label),
			COUNT(DISTINCT source_file)
		FROM facts
	`)
	err := row.Scan(&stats.TotalFacts, &stats.UniqueEntities, &stats.UniquePeriods, &stats.UniqueLabels, &stats.UniqueSources)
	if err != nil {
		return stats, fmt.Errorf("facts.Stats: %w", err)
	}
	return stats, nil
}
