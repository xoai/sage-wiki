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

// Upsert: full-column parity with compiler/items.go:40-69 — sticky pass
// flags preserved only when the hash is unchanged AND the stored flag is 1;
// hash change takes excluded flags (zeroed by the caller = re-process).
func (s *itemStore) Upsert(item store.CompileItem) error {
	var tierOverride any
	if item.TierOverride != nil {
		tierOverride = *item.TierOverride
	}
	var qualityScore any
	if item.QualityScore != nil {
		qualityScore = *item.QualityScore
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
		INSERT INTO compile_items (
			source_path, hash, file_type, size_bytes,
			tier, tier_default, tier_override,
			pass_indexed, pass_embedded, pass_parsed,
			pass_summarized, pass_extracted, pass_written,
			compile_id, error, error_count, summary_path,
			query_hit_count, last_queried_at, promoted_at, demoted_at,
			source_type, quality_score, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23, now(), now())
		ON CONFLICT (source_path) DO UPDATE SET
			hash=excluded.hash, file_type=excluded.file_type, size_bytes=excluded.size_bytes,
			tier=excluded.tier, tier_default=excluded.tier_default, tier_override=excluded.tier_override,
			pass_indexed=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_indexed = 1
				THEN 1 ELSE excluded.pass_indexed END,
			pass_embedded=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_embedded = 1
				THEN 1 ELSE excluded.pass_embedded END,
			pass_parsed=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_parsed = 1
				THEN 1 ELSE excluded.pass_parsed END,
			pass_summarized=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_summarized = 1
				THEN 1 ELSE excluded.pass_summarized END,
			pass_extracted=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_extracted = 1
				THEN 1 ELSE excluded.pass_extracted END,
			pass_written=CASE WHEN compile_items.hash = excluded.hash AND compile_items.pass_written = 1
				THEN 1 ELSE excluded.pass_written END,
			compile_id=excluded.compile_id, error=excluded.error, error_count=excluded.error_count,
			summary_path=excluded.summary_path, source_type=excluded.source_type,
			quality_score=excluded.quality_score, updated_at=now()`,
			item.SourcePath, item.Hash, item.FileType, item.SizeBytes,
			item.Tier, item.TierDefault, tierOverride,
			boolInt(item.PassIndexed), boolInt(item.PassEmbedded), boolInt(item.PassParsed),
			boolInt(item.PassSummarized), boolInt(item.PassExtracted), boolInt(item.PassWritten),
			nullStr(item.CompileID), nullStr(item.Error), item.ErrorCount, nullStr(item.SummaryPath),
			item.QueryHitCount, nullRFC(item.LastQueriedAt), nullRFC(item.PromotedAt), nullRFC(item.DemotedAt),
			nullStr(item.SourceType), qualityScore)
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

// ListPending: the queue read (compiler/items.go:122-135 parity — exact
// per-tier predicate, no upper bound, invalid tier errors).
func (s *itemStore) ListPending(tier int) ([]store.CompileItem, error) {
	var where string
	switch tier {
	case 0:
		where = "tier >= 0 AND pass_indexed = 0"
	case 1:
		where = "tier >= 1 AND pass_embedded = 0"
	case 2:
		where = "tier >= 2 AND pass_parsed = 0"
	case 3:
		where = "tier >= 3 AND (pass_summarized = 0 OR pass_extracted = 0 OR pass_written = 0)"
	default:
		return nil, fmt.Errorf("invalid tier: %d", tier)
	}
	rows, err := s.b.pool.Query(
		"SELECT "+itemCols+" FROM compile_items WHERE "+where+" ORDER BY source_path")
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

// SetTier: idempotent, retains pass flags; promoted_at/demoted_at COALESCE
// per direction; errors on missing source (compiler/items.go:166-195 parity).
func (s *itemStore) SetTier(path string, tier int, reason string) error {
	now := time.Now().UTC()
	return s.b.WriteTx(func(tx *sql.Tx) error {
		var currentTier int
		if err := tx.QueryRow("SELECT tier FROM compile_items WHERE source_path=$1", path).Scan(&currentTier); err != nil {
			return fmt.Errorf("SetTier: source not found: %s", path)
		}
		var promotedAt, demotedAt any
		if tier > currentTier {
			promotedAt = now
		} else if tier < currentTier {
			demotedAt = now
		}
		_, err := tx.Exec(`
			UPDATE compile_items SET tier=$1, promoted_at=COALESCE($2, promoted_at),
				demoted_at=COALESCE($3, demoted_at), updated_at=now()
			WHERE source_path=$4`, tier, promotedAt, demotedAt, path)
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
				"UPDATE compile_items SET query_hit_count=query_hit_count+1, last_queried_at=$1, updated_at=now() WHERE source_path IN (%s)",
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
		"SELECT source_path FROM compile_items WHERE tier IN (0, 1) AND query_hit_count >= $1",
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
