package ontology

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// CommunityStore (P3-5) — SQLite implementation over the shared DB handle.
// Membership is derived state: each detection run replaces it wholesale in
// one tx, preserving summaries for communities whose member set is unchanged.

const communityCols = `id, level, COALESCE(parent_id,''), member_count, edge_count,
	COALESCE(summary,''), COALESCE(summary_hash,''), COALESCE(model,''), updated_at`

func scanCommunities(rows *sql.Rows) ([]store.Community, error) {
	var out []store.Community
	for rows.Next() {
		var c store.Community
		if err := rows.Scan(&c.ID, &c.Level, &c.ParentID, &c.MemberCount, &c.EdgeCount,
			&c.Summary, &c.SummaryHash, &c.Model, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceDetection implements store.CommunityStore. The conditional summary
// clear is computed in Go (the upsert SET can't compare hashes
// conditionally without a stored incoming hash column) — the tx makes the
// read-modify-write atomic.
func (s *Store) ReplaceDetection(comms []store.Community, members map[string][]string) ([]string, error) {
	// Copy before sorting: mutating the caller's slice is a surprising side
	// effect for an interface method.
	comms = append([]store.Community(nil), comms...)
	sort.Slice(comms, func(i, j int) bool { // level-ordered for the parent_id self-FK
		return comms[i].Level < comms[j].Level
	})
	keep := make(map[string]bool, len(comms))
	for _, c := range comms {
		keep[c.ID] = true
	}

	var removed []string
	err := s.db.WriteTx(func(tx *sql.Tx) error {
		// Existing summary state for the conditional clear.
		existing := map[string][3]string{}
		if err := func() error {
			rows, err := tx.Query(`SELECT id, COALESCE(summary_hash,''), COALESCE(summary,''), COALESCE(model,'') FROM communities`)
			if err != nil {
				return fmt.Errorf("ontology.ReplaceDetection: read existing: %w", err)
			}
			defer rows.Close()
			for rows.Next() {
				var id, hash, summary, model string
				if err := rows.Scan(&id, &hash, &summary, &model); err != nil {
					return err
				}
				existing[id] = [3]string{hash, summary, model}
			}
			return rows.Err()
		}(); err != nil {
			return err
		}

		if _, err := tx.Exec(`DELETE FROM community_members`); err != nil {
			return fmt.Errorf("ontology.ReplaceDetection: clear members: %w", err)
		}

		for _, c := range comms {
			hash := store.MemberHash(members[c.ID])
			prev := existing[c.ID]
			summary, summaryHash, model := "", "", ""
			if prev[0] == hash {
				// Unchanged membership: preserve the cached summary (the
				// clear only fires on hash mismatch).
				summary, model = prev[1], prev[2]
				summaryHash = prev[0]
			}
			_, err := tx.Exec(
				`INSERT INTO communities (id, level, parent_id, member_count, edge_count,
				                         summary, summary_hash, model, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET
				   level=excluded.level, parent_id=excluded.parent_id,
				   member_count=excluded.member_count, edge_count=excluded.edge_count,
				   summary=excluded.summary, summary_hash=excluded.summary_hash,
				   model=excluded.model, updated_at=excluded.updated_at`,
				c.ID, c.Level, nilIfEmpty(c.ParentID), c.MemberCount, c.EdgeCount,
				summary, summaryHash, model, c.UpdatedAt)
			if err != nil {
				return fmt.Errorf("ontology.ReplaceDetection: upsert %s: %w", c.ID, err)
			}
		}

		// Delete absent communities, returning their IDs for artifact cleanup.
		for id := range existing {
			if !keep[id] {
				if _, err := tx.Exec(`DELETE FROM communities WHERE id=?`, id); err != nil {
					return fmt.Errorf("ontology.ReplaceDetection: delete %s: %w", id, err)
				}
				removed = append(removed, id)
			}
		}

		for id, ms := range members {
			if !keep[id] {
				continue
			}
			var level int
			for _, c := range comms {
				if c.ID == id {
					level = c.Level
					break
				}
			}
			for _, e := range ms {
				if _, err := tx.Exec(
					`INSERT INTO community_members (community_id, entity_id, level) VALUES (?, ?, ?)`,
					id, e, level); err != nil {
					return fmt.Errorf("ontology.ReplaceDetection: member %s/%s: %w", id, e, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(removed)
	return removed, nil
}

// nilIfEmpty binds empty strings as NULL so SQLite matches the Postgres
// representation (communityCols COALESCEs both to the empty string).
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Store) ListCommunities(level int) ([]store.Community, error) {
	q := `SELECT ` + communityCols + ` FROM communities`
	var args []any
	if level >= 0 {
		q += ` WHERE level=?`
		args = append(args, level)
	}
	q += ` ORDER BY level, id`
	rows, err := s.db.ReadDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommunities(rows)
}

func (s *Store) CommunityMembers(id string) ([]string, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT entity_id FROM community_members WHERE community_id=? ORDER BY entity_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EntityCommunity(entityID string, level int) (string, error) {
	var id string
	err := s.db.ReadDB().QueryRow(
		`SELECT community_id FROM community_members WHERE entity_id=? AND level=?`, entityID, level).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Store) SetSummary(id, summary, summaryHash, model string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE communities SET summary=?, summary_hash=?, model=?, updated_at=? WHERE id=?`,
			summary, summaryHash, model, s.nowUTC().Format(time.RFC3339), id)
		return err
	})
}

func (s *Store) MaxLevel() (int, error) {
	var level sql.NullInt64
	err := s.db.ReadDB().QueryRow(`SELECT MAX(level) FROM communities`).Scan(&level)
	if err != nil {
		return 0, err
	}
	if !level.Valid {
		return -1, nil
	}
	return int(level.Int64), nil
}

func (s *Store) ClearCommunities() error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM community_members`); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM communities`)
		return err
	})
}
