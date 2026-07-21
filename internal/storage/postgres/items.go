package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

type itemStore struct{ b *backend }

var _ store.CompileItemStore = (*itemStore)(nil)

const itemCols = `source_path, hash, file_type, size_bytes, tier, tier_default, tier_override,
	pass_indexed, pass_embedded, pass_parsed, pass_summarized, pass_extracted, pass_written,
	compile_id, error, error_count, summary_path,
	query_hit_count, last_queried_at, promoted_at, demoted_at,
	source_type, quality_score, created_at, updated_at`

func scanItem(row interface{ Scan(...any) error }) (*store.CompileItem, error) {
	var it store.CompileItem
	var hash, ft, cid, errStr, sp, st sql.NullString
	var to sql.NullInt64
	var pi, pe, pp, ps, px, pw int
	var lq, pr, de *time.Time
	var qs sql.NullFloat64
	var created, updated time.Time
	if err := row.Scan(&it.SourcePath, &hash, &ft, &it.SizeBytes, &it.Tier, &it.TierDefault, &to,
		&pi, &pe, &pp, &ps, &px, &pw,
		&cid, &errStr, &it.ErrorCount, &sp,
		&it.QueryHitCount, &lq, &pr, &de,
		&st, &qs, &created, &updated); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	it.Hash, it.FileType, it.CompileID, it.Error, it.SummaryPath, it.SourceType =
		hash.String, ft.String, cid.String, errStr.String, sp.String, st.String
	if to.Valid {
		v := int(to.Int64)
		it.TierOverride = &v
	}
	it.PassIndexed = pi != 0
	it.PassEmbedded = pe != 0
	it.PassParsed = pp != 0
	it.PassSummarized = ps != 0
	it.PassExtracted = px != 0
	it.PassWritten = pw != 0
	it.LastQueriedAt = scanNullRFC(lq)
	it.PromotedAt = scanNullRFC(pr)
	it.DemotedAt = scanNullRFC(de)
	if qs.Valid {
		it.QualityScore = &qs.Float64
	}
	it.CreatedAt = fmtSpace(created)
	it.UpdatedAt = fmtSpace(updated)
	return &it, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Upsert: sticky pass-flag CASE logic parity (items.go:70 — flags are
// preserved across upserts via CASE WHEN excluded.hash <> hash THEN 0 ELSE
// compile_items.pass_X END semantics; hash change resets passes).
func (s *itemStore) Upsert(item store.CompileItem) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
		INSERT INTO compile_items (source_path, hash, file_type, size_bytes, tier, tier_default,
			pass_indexed, pass_embedded, pass_parsed, pass_summarized, pass_extracted, pass_written,
			source_type, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now(), now())
		ON CONFLICT (source_path) DO UPDATE SET
			hash=excluded.hash, file_type=excluded.file_type, size_bytes=excluded.size_bytes,
			updated_at=now(),
			pass_indexed    = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_indexed END,
			pass_embedded   = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_embedded END,
			pass_parsed     = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_parsed END,
			pass_summarized = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_summarized END,
			pass_extracted  = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_extracted END,
			pass_written    = CASE WHEN compile_items.hash <> excluded.hash THEN 0 ELSE compile_items.pass_written END`,
			item.SourcePath, item.Hash, item.FileType, item.SizeBytes, item.Tier, item.TierDefault,
			boolInt(item.PassIndexed), boolInt(item.PassEmbedded), boolInt(item.PassParsed),
			boolInt(item.PassSummarized), boolInt(item.PassExtracted), boolInt(item.PassWritten),
			"compiler")
		return err
	})
}

func (s *itemStore) GetByPath(path string) (*store.CompileItem, error) {
	return scanItem(s.b.pool.QueryRow("SELECT "+itemCols+" FROM compile_items WHERE source_path=$1", path))
}

func (s *itemStore) ListByTier(tier int) ([]store.CompileItem, error) {
	rows, err := s.b.pool.Query("SELECT "+itemCols+" FROM compile_items WHERE tier=$1 ORDER BY source_path", tier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

// ListPending: the queue read — tier >= N AND pass_X = 0 per tier's pass
// (items.go:163 parity: tier 0/1 use pass_indexed, tier 2 pass_parsed,
// tier 3 pass_written).
func (s *itemStore) ListPending(tier int) ([]store.CompileItem, error) {
	var passCol string
	switch tier {
	case 0, 1:
		passCol = "pass_indexed"
	case 2:
		passCol = "pass_parsed"
	default:
		passCol = "pass_written"
	}
	rows, err := s.b.pool.Query(fmt.Sprintf(
		"SELECT "+itemCols+" FROM compile_items WHERE tier >= $1 AND %s = 0 AND tier <= $2 ORDER BY source_path",
		passCol), tier, tier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanItems(rows)
}

func scanItems(rows *sql.Rows) ([]store.CompileItem, error) {
	var out []store.CompileItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

var passColumns = map[string]string{
	"indexed": "pass_indexed", "embedded": "pass_embedded", "parsed": "pass_parsed",
	"summarized": "pass_summarized", "extracted": "pass_extracted", "written": "pass_written",
}

func (s *itemStore) MarkPass(path string, pass string) error {
	col, ok := passColumns[pass]
	if !ok {
		return fmt.Errorf("compiler: unknown pass %q", pass)
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(
			"UPDATE compile_items SET %s=1, updated_at=now() WHERE source_path=$1", col), path)
		return err
	})
}

func (s *itemStore) SetTier(path string, tier int, reason string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET tier=$2, tier_override=$2, updated_at=now() WHERE source_path=$1",
			path, tier)
		return err
	})
}

func (s *itemStore) MarkError(path string, compileErr error) error {
	msg := ""
	if compileErr != nil {
		msg = compileErr.Error()
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET error=$2, error_count=error_count+1, updated_at=now() WHERE source_path=$1",
			path, nullStr(msg))
		return err
	})
}

// IncrementQueryHits: batched IN clauses, 500/chunk (items.go:252 parity).
// last_queried_at is RFC3339-family (spec §5) — written as TIMESTAMPTZ.
func (s *itemStore) IncrementQueryHits(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		now := time.Now().UTC()
		for i := 0; i < len(paths); i += 500 {
			end := i + 500
			if end > len(paths) {
				end = len(paths)
			}
			batch := paths[i:end]
			placeholders := make([]string, len(batch))
			args := []any{now}
			for j, p := range batch {
				placeholders[j] = fmt.Sprintf("$%d", j+2)
				args = append(args, p)
			}
			if _, err := tx.Exec(fmt.Sprintf(
				"UPDATE compile_items SET query_hit_count=query_hit_count+1, last_queried_at=$1 WHERE source_path IN (%s)",
				strings.Join(placeholders, ",")), args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *itemStore) Stats() (*store.CompileStats, error) {
	stats := &store.CompileStats{ByTier: map[int]int{}, BySourceType: map[string]int{}}

	rows, err := s.b.pool.Query("SELECT tier, COUNT(*) FROM compile_items GROUP BY tier")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tier, count int
		if err := rows.Scan(&tier, &count); err != nil {
			rows.Close()
			return nil, err
		}
		stats.ByTier[tier] = count
		stats.TotalSources += count
	}
	rows.Close()

	rows, err = s.b.pool.Query("SELECT source_type, COUNT(*) FROM compile_items GROUP BY source_type")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var count int
		if err := rows.Scan(&st, &count); err != nil {
			rows.Close()
			return nil, err
		}
		stats.BySourceType[st] = count
	}
	rows.Close()

	if err := s.b.pool.QueryRow("SELECT COUNT(*) FROM compile_items WHERE pass_written = 1").Scan(&stats.FullyCompiled); err != nil {
		return nil, err
	}
	if err := s.b.pool.QueryRow("SELECT COUNT(*) FROM compile_items WHERE error IS NOT NULL AND error != ''").Scan(&stats.WithErrors); err != nil {
		return nil, err
	}
	var avgQ sql.NullFloat64
	if err := s.b.pool.QueryRow("SELECT AVG(quality_score) FROM compile_items WHERE quality_score IS NOT NULL").Scan(&avgQ); err != nil {
		return nil, err
	}
	if avgQ.Valid {
		stats.AvgQuality = avgQ.Float64
	}
	return stats, nil
}

func (s *itemStore) DeleteByPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		for i := 0; i < len(paths); i += 500 {
			end := i + 500
			if end > len(paths) {
				end = len(paths)
			}
			batch := paths[i:end]
			placeholders := make([]string, len(batch))
			args := make([]any, len(batch))
			for j, p := range batch {
				placeholders[j] = fmt.Sprintf("$%d", j+1)
				args[j] = p
			}
			if _, err := tx.Exec(fmt.Sprintf(
				"DELETE FROM compile_items WHERE source_path IN (%s)",
				strings.Join(placeholders, ",")), args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *itemStore) SetQualityScore(path string, score float64) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET quality_score=$2, updated_at=now() WHERE source_path=$1", path, score)
		return err
	})
}

func (s *itemStore) Count() (int, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM compile_items").Scan(&n)
	return n, err
}

func (s *itemStore) ListPromotionCandidates(hitThreshold int) ([]string, error) {
	rows, err := s.b.pool.Query(
		"SELECT source_path FROM compile_items WHERE tier < 3 AND query_hit_count >= $1 ORDER BY query_hit_count DESC",
		hitThreshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStrings(rows)
}

// ListDemotionCandidates: three-branch staleness (items.go:434 parity) with
// the pinned quirk reproduction (spec §5): created_at is compared as
// RENDERED TEXT against the RFC3339 threshold (' ' < 'T' lexicographic
// quirk preserved byte-for-byte via to_char), and '' ↔ NULL becomes IS NULL.
func (s *itemStore) ListDemotionCandidates(staleThreshold string) ([]string, error) {
	rows, err := s.b.pool.Query(
		`SELECT source_path FROM compile_items WHERE tier = 3
		 AND (
		   (last_queried_at IS NOT NULL AND to_char(last_queried_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') < $1)
		   OR (last_queried_at IS NULL AND to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') < $1)
		 )`,
		staleThreshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStrings(rows)
}

func (s *itemStore) ListBelowQualityScore(threshold float64) ([]store.QualityScoreRow, error) {
	rows, err := s.b.pool.Query(
		"SELECT source_path, quality_score FROM compile_items WHERE quality_score IS NOT NULL AND quality_score < $1",
		threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.QualityScoreRow
	for rows.Next() {
		var r store.QualityScoreRow
		if err := rows.Scan(&r.SourcePath, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
