package compiler

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// CompileItem represents a source file's compilation state.
// CompileItem is aliased to store.CompileItem (P2-1 D2-prime relocation).
type CompileItem = store.CompileItem

// CompileStats holds tier distribution and progress statistics.
type CompileStats = store.CompileStats

// CompileItemStore provides CRUD operations for the compile_items table.
type CompileItemStore struct {
	db store.DBHandle
	// now is the REQUIRED injected clock (SPEC-04 D4/F-032): there is no
	// time.Now default precisely so no caller can silently leak wall-clock
	// past the SDE-aware clock into DB bytes. Callers pass config.NowUTC.
	now func() time.Time
}

// NewCompileItemStore creates a new CompileItemStore. The clock is mandatory;
// compile paths pass config.NowUTC (SOURCE_DATE_EPOCH-aware).
func NewCompileItemStore(db store.DBHandle, now func() time.Time) *CompileItemStore {
	return &CompileItemStore{db: db, now: now}
}

// dbNow renders the injected clock in SQLite's datetime('now') text format,
// keeping old and new rows lexicographically comparable.
func (s *CompileItemStore) dbNow() string {
	return s.now().UTC().Format("2006-01-02 15:04:05")
}

// Upsert inserts or updates a compile item.
func (s *CompileItemStore) Upsert(item CompileItem) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		var tierOverride sql.NullInt64
		if item.TierOverride != nil {
			tierOverride = sql.NullInt64{Int64: int64(*item.TierOverride), Valid: true}
		}
		var qualityScore sql.NullFloat64
		if item.QualityScore != nil {
			qualityScore = sql.NullFloat64{Float64: *item.QualityScore, Valid: true}
		}
		_, err := tx.Exec(`
			INSERT INTO compile_items (
				source_path, hash, file_type, size_bytes,
				tier, tier_default, tier_override,
				pass_indexed, pass_embedded, pass_parsed,
				pass_summarized, pass_extracted, pass_written,
				compile_id, error, error_count, summary_path,
				query_hit_count, last_queried_at, promoted_at, demoted_at,
				source_type, quality_score, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_path) DO UPDATE SET
				hash=excluded.hash, file_type=excluded.file_type, size_bytes=excluded.size_bytes,
				tier=excluded.tier, tier_default=excluded.tier_default, tier_override=excluded.tier_override,
				-- Sticky pass flags: preserve an existing pass=1 when the hash is
				-- unchanged so an interrupted compile can resume without redoing
				-- completed tiers (issue #88). When the hash differs, the file was
				-- modified and the row's flags are taken from excluded (zeroed by
				-- the caller in pipeline.go) so the source is re-processed.
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
			quality_score=excluded.quality_score, updated_at=?,
			-- Queue revival (P2-3): a hash change means new content — reset the
			-- item to pending with a fresh attempt budget and no lease, so a
			-- fixed dead-lettered source retries. Same-hash upserts never
			-- touch queue state.
			status=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.status ELSE 'pending' END,
			attempts=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.attempts ELSE 0 END,
			lease_owner=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.lease_owner ELSE NULL END,
			lease_until=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.lease_until ELSE NULL END,
			heartbeat_at=CASE WHEN compile_items.hash = excluded.hash THEN compile_items.heartbeat_at ELSE NULL END
		`,
			item.SourcePath, item.Hash, item.FileType, item.SizeBytes,
			item.Tier, item.TierDefault, tierOverride,
			boolToInt(item.PassIndexed), boolToInt(item.PassEmbedded), boolToInt(item.PassParsed),
			boolToInt(item.PassSummarized), boolToInt(item.PassExtracted), boolToInt(item.PassWritten),
			item.CompileID, item.Error, item.ErrorCount, item.SummaryPath,
			item.QueryHitCount, nilIfEmpty(item.LastQueriedAt), nilIfEmpty(item.PromotedAt), nilIfEmpty(item.DemotedAt),
			item.SourceType, qualityScore, s.dbNow(), s.dbNow(), s.dbNow(),
		)
		return err
	})
}

// compileItemCols is the shared SELECT column list for compile_items reads
// (postgres parity: internal/storage/postgres/items.go itemCols).
const compileItemCols = `source_path, hash, file_type, size_bytes,
	tier, tier_default, tier_override,
	pass_indexed, pass_embedded, pass_parsed,
	pass_summarized, pass_extracted, pass_written,
	compile_id, error, error_count, summary_path,
	query_hit_count, last_queried_at, promoted_at, demoted_at,
	source_type, quality_score,
	status, lease_owner, lease_until, heartbeat_at, attempts,
	compile_key, compile_key_parts,
	created_at, updated_at`

// GetByPath returns a single compile item.
func (s *CompileItemStore) GetByPath(path string) (*CompileItem, error) {
	row := s.db.ReadDB().QueryRow(`SELECT `+compileItemCols+`
		FROM compile_items WHERE source_path = ?
	`, path)
	return scanCompileItem(row)
}

// ListByTier returns all items at a given tier.
func (s *CompileItemStore) ListByTier(tier int) ([]CompileItem, error) {
	rows, err := s.db.ReadDB().Query(`SELECT `+compileItemCols+`
		FROM compile_items WHERE tier = ?
	`, tier)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCompileItems(rows)
}

// ListPending returns items that need work at their current tier.
// For Tier 0: pass_indexed=0. For Tier 1: pass_embedded=0.
// For Tier 3: any of pass_summarized/pass_extracted/pass_written=0.
func (s *CompileItemStore) ListPending(tier int) ([]CompileItem, error) {
	where, err := pendingWhere(tier)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.ReadDB().Query(fmt.Sprintf(`SELECT `+compileItemCols+`
		FROM compile_items WHERE %s
	`, where))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCompileItems(rows)
}

// MarkPass marks a specific pass complete for a source.
func (s *CompileItemStore) MarkPass(path string, pass string) error {
	col, ok := passColumn(pass)
	if !ok {
		return fmt.Errorf("unknown pass: %s", pass)
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(
			"UPDATE compile_items SET %s = 1, updated_at = ? WHERE source_path = ?", col,
		), s.dbNow(), path)
		return err
	})
}

// SetTier changes an item's tier. Idempotent — retains existing pass flags.
// A source promoted from Tier 1 to Tier 3 keeps pass_indexed=1, pass_embedded=1.
func (s *CompileItemStore) SetTier(path string, tier int, reason string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		now := s.now().UTC().Format(time.RFC3339)
		// Determine if this is promotion or demotion for timestamp fields
		var currentTier int
		err := tx.QueryRow("SELECT tier FROM compile_items WHERE source_path = ?", path).Scan(&currentTier)
		if err != nil {
			return fmt.Errorf("SetTier: source not found: %s", path)
		}

		promotedAt := sql.NullString{}
		demotedAt := sql.NullString{}
		if tier > currentTier {
			promotedAt = sql.NullString{String: now, Valid: true}
		} else if tier < currentTier {
			demotedAt = sql.NullString{String: now, Valid: true}
		}

		_, err = tx.Exec(`
			UPDATE compile_items SET tier = ?, promoted_at = COALESCE(?, promoted_at),
				demoted_at = COALESCE(?, demoted_at), updated_at = ?
			WHERE source_path = ?
		`, tier, promotedAt, demotedAt, s.dbNow(), path)
		return err
	})
}

// MarkError records an error for a source and increments the error count.
func (s *CompileItemStore) MarkError(path string, compileErr error) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE compile_items SET error = ?, error_count = error_count + 1,
				updated_at = ? WHERE source_path = ?
		`, compileErr.Error(), s.dbNow(), path)
		return err
	})
}

// IncrementQueryHits increments hit counts for the given source paths.
// Uses batch IN clauses for efficiency, chunked at 500 paths to stay
// well under SQLite's parameter limit.
func (s *CompileItemStore) IncrementQueryHits(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		now := s.now().UTC().Format(time.RFC3339)
		for _, chunk := range chunkStrings(paths, 500) {
			placeholders, args := buildInClause(chunk)
			// Prepend now to args (for last_queried_at)
			allArgs := make([]interface{}, 0, 2+len(args))
			allArgs = append(allArgs, now, s.dbNow())
			allArgs = append(allArgs, args...)
			_, err := tx.Exec(`
				UPDATE compile_items SET query_hit_count = query_hit_count + 1,
					last_queried_at = ?, updated_at = ?
				WHERE source_path IN (`+placeholders+`)
			`, allArgs...)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Stats returns tier distribution and compilation progress.
func (s *CompileItemStore) Stats() (*CompileStats, error) {
	stats := &CompileStats{
		ByTier:       make(map[int]int),
		BySourceType: make(map[string]int),
		ByStatus:     make(map[string]int),
	}

	// Tier distribution
	rows, err := s.db.ReadDB().Query("SELECT tier, COUNT(*) FROM compile_items GROUP BY tier")
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

	// Source type distribution
	rows, err = s.db.ReadDB().Query("SELECT source_type, COUNT(*) FROM compile_items GROUP BY source_type")
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

	// Fully compiled count
	err = s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM compile_items WHERE pass_written = 1").Scan(&stats.FullyCompiled)
	if err != nil {
		return nil, err
	}

	// Error count
	err = s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM compile_items WHERE error IS NOT NULL AND error != ''").Scan(&stats.WithErrors)
	if err != nil {
		return nil, err
	}

	// Average quality score
	var avgQ sql.NullFloat64
	err = s.db.ReadDB().QueryRow("SELECT AVG(quality_score) FROM compile_items WHERE quality_score IS NOT NULL").Scan(&avgQ)
	if err != nil {
		return nil, err
	}
	if avgQ.Valid {
		stats.AvgQuality = avgQ.Float64
	}

	// Queue state (P2-3)
	rows, err = s.db.ReadDB().Query("SELECT status, COUNT(*) FROM compile_items GROUP BY status")
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
	var owner, heartbeat sql.NullString
	err = s.db.ReadDB().QueryRow(`SELECT lease_owner, MAX(heartbeat_at) FROM compile_items
		WHERE status = 'leased' AND lease_owner IS NOT NULL GROUP BY lease_owner
		ORDER BY 2 DESC LIMIT 1`).Scan(&owner, &heartbeat)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	stats.ActiveOwner = owner.String
	stats.LastHeartbeat = heartbeat.String

	return stats, nil
}

// DeleteByPaths removes compile items for the given paths.
// Uses batch IN clauses for efficiency, chunked at 500 paths.
func (s *CompileItemStore) DeleteByPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		for _, chunk := range chunkStrings(paths, 500) {
			placeholders, args := buildInClause(chunk)
			_, err := tx.Exec("DELETE FROM compile_items WHERE source_path IN ("+placeholders+")", args...)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// buildInClause builds a parameterized IN clause: "?, ?, ?" and []interface{}{a, b, c}.
func buildInClause(values []string) (string, []interface{}) {
	args := make([]interface{}, len(values))
	placeholders := make([]byte, 0, len(values)*2)
	for i, v := range values {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = v
	}
	return string(placeholders), args
}

// chunkStrings splits a slice into chunks of at most size n.
func chunkStrings(s []string, n int) [][]string {
	if len(s) <= n {
		return [][]string{s}
	}
	var chunks [][]string
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

// SetQualityScore updates the quality_score for a source.
func (s *CompileItemStore) SetQualityScore(path string, score float64) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET quality_score = ?, updated_at = ? WHERE source_path = ?",
			score, s.dbNow(), path,
		)
		return err
	})
}

// Count returns the total number of compile items.
func (s *CompileItemStore) Count() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM compile_items").Scan(&count)
	return count, err
}

// ListPromotionCandidates returns Tier 0-1 source paths with query_hit_count
// at or above the given threshold. Filtering is done in SQL to avoid loading
// all low-tier items into memory at scale.
func (s *CompileItemStore) ListPromotionCandidates(hitThreshold int) ([]string, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT source_path FROM compile_items
		 WHERE tier IN (0, 1) AND query_hit_count >= ?`,
		hitThreshold,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// ListDemotionCandidates returns Tier 3 source paths that are stale —
// either last queried before the threshold date, or never queried and
// created before the threshold date. Filtering is done in SQL.
func (s *CompileItemStore) ListDemotionCandidates(staleThreshold string) ([]string, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT source_path FROM compile_items WHERE tier = 3
		 AND (
		   (last_queried_at != '' AND last_queried_at < ?)
		   OR (last_queried_at IS NULL AND created_at < ?)
		   OR (last_queried_at = '' AND created_at < ?)
		 )`,
		staleThreshold, staleThreshold, staleThreshold,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// helpers

func passColumn(pass string) (string, bool) {
	switch pass {
	case "indexed":
		return "pass_indexed", true
	case "embedded":
		return "pass_embedded", true
	case "parsed":
		return "pass_parsed", true
	case "summarized":
		return "pass_summarized", true
	case "extracted":
		return "pass_extracted", true
	case "written":
		return "pass_written", true
	default:
		return "", false
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanCompileItem(row *sql.Row) (*CompileItem, error) {
	var item CompileItem
	var tierOverride sql.NullInt64
	var qualityScore sql.NullFloat64
	var compileID, errStr, summaryPath, lastQueried, promoted, demoted sql.NullString
	var leaseOwner, leaseUntil, heartbeatAt sql.NullString
	var passIdx, passEmbed, passParse, passSum, passExt, passWrite int

	err := row.Scan(
		&item.SourcePath, &item.Hash, &item.FileType, &item.SizeBytes,
		&item.Tier, &item.TierDefault, &tierOverride,
		&passIdx, &passEmbed, &passParse, &passSum, &passExt, &passWrite,
		&compileID, &errStr, &item.ErrorCount, &summaryPath,
		&item.QueryHitCount, &lastQueried, &promoted, &demoted,
		&item.SourceType, &qualityScore,
		&item.Status, &leaseOwner, &leaseUntil, &heartbeatAt, &item.Attempts,
		&item.CompileKey, &item.CompileKeyParts,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if tierOverride.Valid {
		v := int(tierOverride.Int64)
		item.TierOverride = &v
	}
	if qualityScore.Valid {
		item.QualityScore = &qualityScore.Float64
	}
	item.PassIndexed = passIdx == 1
	item.PassEmbedded = passEmbed == 1
	item.PassParsed = passParse == 1
	item.PassSummarized = passSum == 1
	item.PassExtracted = passExt == 1
	item.PassWritten = passWrite == 1
	item.CompileID = compileID.String
	item.Error = errStr.String
	item.SummaryPath = summaryPath.String
	item.LastQueriedAt = lastQueried.String
	item.PromotedAt = promoted.String
	item.DemotedAt = demoted.String
	item.LeaseOwner = leaseOwner.String
	item.LeaseUntil = leaseUntil.String
	item.HeartbeatAt = heartbeatAt.String

	return &item, nil
}

func scanCompileItems(rows *sql.Rows) ([]CompileItem, error) {
	var items []CompileItem
	for rows.Next() {
		var item CompileItem
		var tierOverride sql.NullInt64
		var qualityScore sql.NullFloat64
		var compileID, errStr, summaryPath, lastQueried, promoted, demoted sql.NullString
		var leaseOwner, leaseUntil, heartbeatAt sql.NullString
		var passIdx, passEmbed, passParse, passSum, passExt, passWrite int

		err := rows.Scan(
			&item.SourcePath, &item.Hash, &item.FileType, &item.SizeBytes,
			&item.Tier, &item.TierDefault, &tierOverride,
			&passIdx, &passEmbed, &passParse, &passSum, &passExt, &passWrite,
			&compileID, &errStr, &item.ErrorCount, &summaryPath,
			&item.QueryHitCount, &lastQueried, &promoted, &demoted,
			&item.SourceType, &qualityScore,
			&item.Status, &leaseOwner, &leaseUntil, &heartbeatAt, &item.Attempts,
			&item.CompileKey, &item.CompileKeyParts,
			&item.CreatedAt, &item.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		if tierOverride.Valid {
			v := int(tierOverride.Int64)
			item.TierOverride = &v
		}
		if qualityScore.Valid {
			item.QualityScore = &qualityScore.Float64
		}
		item.PassIndexed = passIdx == 1
		item.PassEmbedded = passEmbed == 1
		item.PassParsed = passParse == 1
		item.PassSummarized = passSum == 1
		item.PassExtracted = passExt == 1
		item.PassWritten = passWrite == 1
		item.CompileID = compileID.String
		item.Error = errStr.String
		item.SummaryPath = summaryPath.String
		item.LastQueriedAt = lastQueried.String
		item.PromotedAt = promoted.String
		item.DemotedAt = demoted.String
		item.LeaseOwner = leaseOwner.String
		item.LeaseUntil = leaseUntil.String
		item.HeartbeatAt = heartbeatAt.String

		items = append(items, item)
	}
	return items, rows.Err()
}

// --- Durable queue (P2-3, spec C2) ---

// pendingWhere mirrors ListPending's per-tier predicate (tiers 0-3).
func pendingWhere(tier int) (string, error) {
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

// Claim leases up to limit pending items at a tier for owner. Fencing is a
// conditional UPDATE per candidate: an item whose lease state changed since
// the candidate scan affects 0 rows and is skipped — no double-claim.
// Lease timestamps are RFC3339 UTC so TEXT comparisons sort correctly.
func (s *CompileItemStore) Claim(tier int, owner string, ttl time.Duration, limit int) ([]CompileItem, error) {
	where, err := pendingWhere(tier)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	nowStr := now.Format(time.RFC3339)
	untilStr := now.Add(ttl).Format(time.RFC3339)

	var claimed []CompileItem
	return claimed, s.db.WriteTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(fmt.Sprintf(`SELECT `+compileItemCols+`
			FROM compile_items
			WHERE %s AND status != 'failed'
				AND (lease_until IS NULL OR lease_until < ? OR lease_owner = ?)
			ORDER BY source_path LIMIT ?
		`, where), nowStr, owner, limit)
		if err != nil {
			return err
		}
		candidates, err := scanCompileItems(rows)
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range candidates {
			res, err := tx.Exec(`
				UPDATE compile_items SET status = 'leased', lease_owner = ?,
					lease_until = ?, heartbeat_at = ?,
					updated_at = ?
				WHERE source_path = ?
					AND (lease_until IS NULL OR lease_until < ? OR lease_owner = ?)
			`, owner, untilStr, nowStr, s.dbNow(), c.SourcePath, nowStr, owner)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				continue // lost the race — another owner holds it now
			}
			c.Status = "leased"
			c.LeaseOwner = owner
			c.LeaseUntil = untilStr
			c.HeartbeatAt = nowStr
			claimed = append(claimed, c)
		}
		return nil
	})
}

// Heartbeat refreshes the lease on items still owned by owner.
func (s *CompileItemStore) Heartbeat(owner string, paths []string, ttl time.Duration) error {
	if len(paths) == 0 {
		return nil
	}
	now := s.now().UTC()
	nowStr := now.Format(time.RFC3339)
	untilStr := now.Add(ttl).Format(time.RFC3339)
	return s.db.WriteTx(func(tx *sql.Tx) error {
		for _, chunk := range chunkStrings(paths, 500) {
			placeholders, args := buildInClause(chunk)
			allArgs := append([]interface{}{nowStr, untilStr, s.dbNow(), owner}, args...)
			if _, err := tx.Exec(`
				UPDATE compile_items SET heartbeat_at = ?, lease_until = ?,
					updated_at = ?
				WHERE lease_owner = ? AND source_path IN (`+placeholders+`)
			`, allArgs...); err != nil {
				return err
			}
		}
		return nil
	})
}

// tierComplete reports whether every pass applicable to the item's tier is
// done — the same predicate as the V9 backfill (spec C1).
func tierComplete(it *CompileItem) bool {
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
	default: // tier 3
		return it.PassEmbedded && it.PassSummarized && it.PassExtracted && it.PassWritten
	}
}

// Release clears the lease and sets the outcome status. ReleaseDone is
// tier-complete-aware: an item that still owes passes returns to pending.
// The lease_owner match fences stale workers out of state flips.
func (s *CompileItemStore) Release(path string, owner string, outcome store.ReleaseOutcome) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		var status string
		switch outcome {
		case store.ReleaseDone:
			row := tx.QueryRow(`SELECT `+compileItemCols+`
				FROM compile_items WHERE source_path = ?`, path)
			it, err := scanCompileItem(row)
			if err != nil {
				return err
			}
			if it == nil {
				return fmt.Errorf("Release: source not found: %s", path)
			}
			if tierComplete(it) {
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
		// attempts counts FAILED processing attempts (not claims): a retry
		// burns budget, a done-outcome (even with passes still owed = the
		// item is progressing) resets it. Otherwise partial-progress items
		// and systemic outages would dead-letter healthy sources.
		attemptsSQL := "attempts = 0"
		if outcome == store.ReleaseRetry {
			attemptsSQL = "attempts = attempts + 1"
		}
		_, err := tx.Exec(fmt.Sprintf(`
			UPDATE compile_items SET status = ?, lease_owner = NULL,
				lease_until = NULL, heartbeat_at = NULL, %s, updated_at = ?
			WHERE source_path = ? AND lease_owner = ?
		`, attemptsSQL), status, s.dbNow(), path, owner)
		return err
	})
}

// RequeueExpired returns items whose leases expired to pending (crash
// recovery — spec C7). Returns the number requeued.
func (s *CompileItemStore) RequeueExpired(now time.Time) (int, error) {
	var n int64
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE compile_items SET status = 'pending', lease_owner = NULL,
				lease_until = NULL, heartbeat_at = NULL, updated_at = ?
			WHERE status = 'leased' AND lease_until < ?
		`, s.dbNow(), now.UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// SetCompileKey stores the computed compile key and its component preimages
// for a source (SPEC-04). Set at the doc's final-pass completion.
func (s *CompileItemStore) SetCompileKey(path, key, partsJSON string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET compile_key = ?, compile_key_parts = ?, updated_at = ? WHERE source_path = ?",
			key, partsJSON, s.dbNow(), path,
		)
		return err
	})
}

// InvalidatePasses zeroes every pass flag (SPEC-04 R5/R1): a key-drifted
// or --forced doc recompiles all passes. The stored key is LEFT in place —
// it is the old-value evidence --explain diffs against; the new key
// overwrites it at completion.
func (s *CompileItemStore) InvalidatePasses(path string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE compile_items SET
			pass_indexed = 0, pass_embedded = 0, pass_parsed = 0,
			pass_summarized = 0, pass_extracted = 0, pass_written = 0,
			updated_at = ?
			WHERE source_path = ?`, s.dbNow(), path)
		return err
	})
}

// ClearCompileKey drops a source's stored key (SPEC-04): called where the
// existing removal handling runs, so a re-added doc compiles fresh.
func (s *CompileItemStore) ClearCompileKey(path string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE compile_items SET compile_key = '', compile_key_parts = '', updated_at = ? WHERE source_path = ?",
			s.dbNow(), path,
		)
		return err
	})
}

// ResetFailed revives dead-lettered items with a fresh attempt budget
// (used by compile --fresh).
func (s *CompileItemStore) ResetFailed() (int, error) {
	var n int64
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE compile_items SET status = 'pending', attempts = 0,
				lease_owner = NULL, lease_until = NULL, heartbeat_at = NULL,
				updated_at = ?
			WHERE status = 'failed'
		`, s.dbNow())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

// QualityScoreRow is a (source_path, quality_score) pair.
type QualityScoreRow = store.QualityScoreRow

// ListBelowQualityScore returns items with a non-NULL quality_score below
// threshold (P2-1: absorbs linter's low-quality scan, passes.go:432).
func (s *CompileItemStore) ListBelowQualityScore(threshold float64) ([]QualityScoreRow, error) {
	rows, err := s.db.ReadDB().Query(
		"SELECT source_path, quality_score FROM compile_items WHERE quality_score IS NOT NULL AND quality_score < ?",
		threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []QualityScoreRow
	for rows.Next() {
		var r QualityScoreRow
		if err := rows.Scan(&r.SourcePath, &r.Score); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
