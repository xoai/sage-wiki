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

// nowUTC is the artifact clock (SPEC-04 D4): OpenOptions.Now when the
// opener supplied one (compile paths pass config.NowUTC), else wall clock —
// the fallback is acceptable only because no compile path opens postgres
// today (compiler uses the concrete SQLite handle).
func (s *itemStore) nowUTC() time.Time {
	if s.b.opts.Now != nil {
		return s.b.opts.Now().UTC()
	}
	return time.Now().UTC()
}

const itemCols = `source_path, hash, file_type, size_bytes, tier, tier_default, tier_override,
	pass_indexed, pass_embedded, pass_parsed, pass_summarized, pass_extracted, pass_written,
	compile_id, error, error_count, summary_path,
	query_hit_count, last_queried_at, promoted_at, demoted_at,
	source_type, quality_score,
	status, lease_owner, lease_until, heartbeat_at, attempts,
	compile_key, compile_key_parts,
	created_at, updated_at`

func scanItem(row interface{ Scan(...any) error }) (*store.CompileItem, error) {
	var it store.CompileItem
	var hash, ft, cid, errStr, sp, st sql.NullString
	var lo sql.NullString
	var lu2, hb2 *time.Time
	var to sql.NullInt64
	var pi, pe, pp, ps, px, pw int
	var lq, pr, de *time.Time
	var qs sql.NullFloat64
	var created, updated time.Time
	if err := row.Scan(&it.SourcePath, &hash, &ft, &it.SizeBytes, &it.Tier, &it.TierDefault, &to,
		&pi, &pe, &pp, &ps, &px, &pw,
		&cid, &errStr, &it.ErrorCount, &sp,
		&it.QueryHitCount, &lq, &pr, &de,
		&st, &qs,
		&it.Status, &lo, &lu2, &hb2, &it.Attempts,
		&it.CompileKey, &it.CompileKeyParts,
		&created, &updated); err != nil {
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
	it.LeaseOwner = lo.String
	it.LeaseUntil = scanNullRFC(lu2)
	it.HeartbeatAt = scanNullRFC(hb2)
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
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
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
		quality_score=excluded.quality_score, updated_at=$26,
		-- Queue revival (P2-3): hash change resets queue state; same-hash
		-- upserts never touch it (compiler/items.go parity).
		status=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.status ELSE 'pending' END,
		attempts=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.attempts ELSE 0 END,
		lease_owner=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.lease_owner ELSE NULL END,
		lease_until=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.lease_until ELSE NULL END,
		heartbeat_at=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.heartbeat_at ELSE NULL END`,
			item.SourcePath, item.Hash, item.FileType, item.SizeBytes,
			item.Tier, item.TierDefault, tierOverride,
			boolInt(item.PassIndexed), boolInt(item.PassEmbedded), boolInt(item.PassParsed),
			boolInt(item.PassSummarized), boolInt(item.PassExtracted), boolInt(item.PassWritten),
			nullStr(item.CompileID), nullStr(item.Error), item.ErrorCount, nullStr(item.SummaryPath),
			item.QueryHitCount, nullRFC(item.LastQueriedAt), nullRFC(item.PromotedAt), nullRFC(item.DemotedAt),
			item.SourceType, qualityScore, s.nowUTC(), s.nowUTC(), s.nowUTC())
		return err
	})
}

// SetCompileKey stores the computed compile key + preimages (SPEC-04).
func (s *itemStore) SetCompileKey(path, key, partsJSON string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET compile_key=$2, compile_key_parts=$3, updated_at=$4 WHERE source_path=$1",
			path, key, partsJSON, s.nowUTC())
		return err
	})
}

// InvalidatePasses zeroes every pass flag (SPEC-04 R5/R1).
func (s *itemStore) InvalidatePasses(path string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE compile_items SET
			pass_indexed=0, pass_embedded=0, pass_parsed=0,
			pass_summarized=0, pass_extracted=0, pass_written=0,
			updated_at=$2 WHERE source_path=$1`, path, s.nowUTC())
		return err
	})
}

// ClearCompileKey drops a source's stored key (SPEC-04).
func (s *itemStore) ClearCompileKey(path string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET compile_key='', compile_key_parts='', updated_at=$2 WHERE source_path=$1",
			path, s.nowUTC())
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
		"SELECT " + itemCols + " FROM compile_items WHERE " + where + " ORDER BY source_path")
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
			"UPDATE compile_items SET %s=1, updated_at=$2 WHERE source_path=$1", col), path, s.nowUTC())
		return err
	})
}

// SetTier: idempotent, retains pass flags; promoted_at/demoted_at COALESCE
// per direction; errors on missing source (compiler/items.go:166-195 parity).
func (s *itemStore) SetTier(path string, tier int, reason string) error {
	now := s.nowUTC()
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
				demoted_at=COALESCE($3, demoted_at), updated_at=$5
			WHERE source_path=$4`, tier, promotedAt, demotedAt, path, s.nowUTC())
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
			"UPDATE compile_items SET error=$2, error_count=error_count+1, updated_at=$3 WHERE source_path=$1",
			path, nullStr(msg), s.nowUTC())
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
		now := s.nowUTC()
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
				"UPDATE compile_items SET query_hit_count=query_hit_count+1, last_queried_at=$1, updated_at=$%d WHERE source_path IN (%s)",
				len(batch)+2, strings.Join(placeholders, ",")), append(args, s.nowUTC())...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *itemStore) Stats() (*store.CompileStats, error) {
	stats := &store.CompileStats{ByTier: map[int]int{}, BySourceType: map[string]int{}, ByStatus: map[string]int{}}

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

	// Queue state (P2-3, sqlite parity)
	rows, err = s.b.pool.Query("SELECT status, COUNT(*) FROM compile_items GROUP BY status")
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
		stats.ByStatus[st] = count
	}
	rows.Close()
	var owner *string
	var heartbeat *time.Time
	if err := s.b.pool.QueryRow(`SELECT lease_owner, MAX(heartbeat_at) FROM compile_items
		WHERE status = 'leased' AND lease_owner IS NOT NULL GROUP BY lease_owner
		ORDER BY 2 DESC LIMIT 1`).Scan(&owner, &heartbeat); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if owner != nil {
		stats.ActiveOwner = *owner
	}
	if heartbeat != nil {
		stats.LastHeartbeat = heartbeat.UTC().Format(time.RFC3339)
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
			"UPDATE compile_items SET quality_score=$2, updated_at=$3 WHERE source_path=$1", path, score, s.nowUTC())
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
// quirk preserved byte-for-byte via to_char), and ” ↔ NULL becomes IS NULL.
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

// --- Durable queue (P2-3, spec C2 — compiler/items.go parity) ---

// Claim leases up to limit pending items at a tier for owner. Same
// conditional-UPDATE fencing as sqlite: a candidate whose lease changed
// since the scan affects 0 rows and is skipped.
func (s *itemStore) Claim(tier int, owner string, ttl time.Duration, limit int) ([]store.CompileItem, error) {
	where, err := pendingWherePG(tier)
	if err != nil {
		return nil, err
	}
	now := s.nowUTC()
	until := now.Add(ttl)

	var claimed []store.CompileItem
	return claimed, s.b.WriteTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT `+itemCols+`
			FROM compile_items
			WHERE `+where+` AND status != 'failed'
				AND (lease_until IS NULL OR lease_until < $1 OR lease_owner = $2)
			ORDER BY source_path LIMIT $3
		`, now, owner, limit)
		if err != nil {
			return err
		}
		candidates, err := scanItems(rows)
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range candidates {
			res, err := tx.Exec(`
				UPDATE compile_items SET status = 'leased', lease_owner = $1,
					lease_until = $2, heartbeat_at = $3,
					updated_at = $7
				WHERE source_path = $4
					AND (lease_until IS NULL OR lease_until < $5 OR lease_owner = $6)
			`, owner, until, now, c.SourcePath, now, owner, s.nowUTC())
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				continue // lost the race
			}
			c.Status = "leased"
			c.LeaseOwner = owner
			c.LeaseUntil = until.Format(time.RFC3339)
			c.HeartbeatAt = now.Format(time.RFC3339)
			claimed = append(claimed, c)
		}
		return nil
	})
}

func pendingWherePG(tier int) (string, error) {
	switch tier {
	case 0:
		return "tier >= 0 AND pass_indexed = 0", nil
	case 1:
		return "tier >= 1 AND pass_embedded = 0", nil
	case 2:
		return "tier >= 2 AND pass_parsed = 0", nil
	case 3:
		return "tier >= 3 AND (pass_summarized = 0 OR pass_extracted = 0 OR pass_written = 0)", nil
	default:
		return "", fmt.Errorf("invalid tier: %d", tier)
	}
}

// Heartbeat refreshes the lease on items still owned by owner.
func (s *itemStore) Heartbeat(owner string, paths []string, ttl time.Duration) error {
	if len(paths) == 0 {
		return nil
	}
	now := s.nowUTC()
	until := now.Add(ttl)
	return s.b.WriteTx(func(tx *sql.Tx) error {
		for i := 0; i < len(paths); i += 500 {
			batch := paths[i:min(i+500, len(paths))]
			placeholders := make([]string, len(batch))
			args := make([]any, 0, len(batch)+4)
			args = append(args, now, until, s.nowUTC(), owner)
			for j, p := range batch {
				placeholders[j] = fmt.Sprintf("$%d", j+5)
				args = append(args, p)
			}
			if _, err := tx.Exec(`
				UPDATE compile_items SET heartbeat_at = $1, lease_until = $2,
					updated_at = $3
				WHERE lease_owner = $4 AND source_path IN (`+
				strings.Join(placeholders, ",")+`)`, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// Release clears the lease and sets the outcome status (tier-complete-aware
// for ReleaseDone, lease_owner-fenced — compiler/items.go parity).
func (s *itemStore) Release(path string, owner string, outcome store.ReleaseOutcome) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		var status string
		switch outcome {
		case store.ReleaseDone:
			it, err := scanItem(tx.QueryRow(`SELECT `+itemCols+`
				FROM compile_items WHERE source_path = $1`, path))
			if err != nil {
				return err
			}
			if it == nil {
				return fmt.Errorf("Release: source not found: %s", path)
			}
			if tierCompletePG(it) {
				status = "done"
			} else {
				status = "pending"
			}
		case store.ReleaseRetry:
			status = "pending"
		case store.ReleaseFailed:
			status = "failed"
		default:
			return fmt.Errorf("Release: unknown outcome: %d", outcome)
		}
		// attempts counts FAILED processing attempts — see sqlite Release.
		attemptsSQL := "attempts = 0"
		if outcome == store.ReleaseRetry {
			attemptsSQL = "attempts = attempts + 1"
		}
		_, err := tx.Exec(fmt.Sprintf(`
			UPDATE compile_items SET status = $1, lease_owner = NULL,
				lease_until = NULL, heartbeat_at = NULL, %s, updated_at = $4
			WHERE source_path = $2 AND lease_owner = $3
		`, attemptsSQL), status, path, owner, s.nowUTC())
		return err
	})
}

// tierCompletePG mirrors compiler/items.go tierComplete.
func tierCompletePG(it *store.CompileItem) bool {
	if !it.PassIndexed {
		return false
	}
	switch it.Tier {
	case 0:
		return true
	case 1:
		return it.PassEmbedded
	case 2:
		return it.PassEmbedded && it.PassParsed
	default:
		return it.PassEmbedded && it.PassSummarized && it.PassExtracted && it.PassWritten
	}
}

// RequeueExpired returns expired leases to pending (crash recovery).
func (s *itemStore) RequeueExpired(now time.Time) (int, error) {
	var n int64
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE compile_items SET status = 'pending', lease_owner = NULL,
				lease_until = NULL, heartbeat_at = NULL, updated_at = $2
			WHERE status = 'leased' AND lease_until < $1
		`, now.UTC(), s.nowUTC())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// ResetFailed revives dead-lettered items with a fresh attempt budget.
func (s *itemStore) ResetFailed() (int, error) {
	var n int64
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE compile_items SET status = 'pending', attempts = 0,
				lease_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
				updated_at = $1
			WHERE status = 'failed'
		`, s.nowUTC())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
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
