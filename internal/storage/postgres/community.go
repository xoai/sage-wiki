package postgres

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// CommunityStore (P3-5) — Postgres twin of internal/ontology/community.go.
// Semantics identical; placeholder style and binding differ.

type communityStore struct {
	b *backend
}

var _ store.CommunityStore = (*communityStore)(nil)

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

func (s *communityStore) ReplaceDetection(comms []store.Community, members map[string][]string) ([]string, error) {
	comms = append([]store.Community(nil), comms...)
	sort.Slice(comms, func(i, j int) bool {
		return comms[i].Level < comms[j].Level
	})
	keep := make(map[string]bool, len(comms))
	for _, c := range comms {
		keep[c.ID] = true
	}

	var removed []string
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		existing := map[string][3]string{}
		if err := func() error {
			rows, err := tx.Query(`SELECT id, COALESCE(summary_hash,''), COALESCE(summary,''), COALESCE(model,'') FROM communities FOR UPDATE`)
			if err != nil {
				return fmt.Errorf("postgres.ReplaceDetection: read existing: %w", err)
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
			return fmt.Errorf("postgres.ReplaceDetection: clear members: %w", err)
		}

		for _, c := range comms {
			hash := store.MemberHash(members[c.ID])
			prev := existing[c.ID]
			summary, summaryHash, model := "", "", ""
			if prev[0] == hash {
				summary, model = prev[1], prev[2]
				summaryHash = prev[0]
			}
			_, err := tx.Exec(
				`INSERT INTO communities (id, level, parent_id, member_count, edge_count,
				                         summary, summary_hash, model, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				 ON CONFLICT(id) DO UPDATE SET
				   level=excluded.level, parent_id=excluded.parent_id,
				   member_count=excluded.member_count, edge_count=excluded.edge_count,
				   summary=excluded.summary, summary_hash=excluded.summary_hash,
				   model=excluded.model, updated_at=excluded.updated_at`,
				c.ID, c.Level, nullStr(c.ParentID), c.MemberCount, c.EdgeCount,
				nullStr(summary), nullStr(summaryHash), nullStr(model), c.UpdatedAt)
			if err != nil {
				return fmt.Errorf("postgres.ReplaceDetection: upsert %s: %w", c.ID, err)
			}
		}

		for id := range existing {
			if !keep[id] {
				if _, err := tx.Exec(`DELETE FROM communities WHERE id=$1`, id); err != nil {
					return fmt.Errorf("postgres.ReplaceDetection: delete %s: %w", id, err)
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
					`INSERT INTO community_members (community_id, entity_id, level) VALUES ($1, $2, $3)`,
					id, e, level); err != nil {
					return fmt.Errorf("postgres.ReplaceDetection: member %s/%s: %w", id, e, err)
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

func (s *communityStore) ListCommunities(level int) ([]store.Community, error) {
	q := `SELECT ` + communityCols + ` FROM communities`
	var args []any
	if level >= 0 {
		q += ` WHERE level=$1`
		args = append(args, level)
	}
	q += ` ORDER BY level, id`
	rows, err := s.b.pool.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommunities(rows)
}

func (s *communityStore) CommunityMembers(id string) ([]string, error) {
	rows, err := s.b.pool.Query(
		`SELECT entity_id FROM community_members WHERE community_id=$1 ORDER BY entity_id`, id)
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

func (s *communityStore) EntityCommunity(entityID string, level int) (string, error) {
	var id string
	err := s.b.pool.QueryRow(
		`SELECT community_id FROM community_members WHERE entity_id=$1 AND level=$2`, entityID, level).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *communityStore) SetSummary(id, summary, summaryHash, model string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE communities SET summary=$1, summary_hash=$2, model=$3, updated_at=$4 WHERE id=$5`,
			nullStr(summary), nullStr(summaryHash), nullStr(model), time.Now().UTC().Format(time.RFC3339), id)
		return err
	})
}

func (s *communityStore) MaxLevel() (int, error) {
	var level sql.NullInt64
	err := s.b.pool.QueryRow(`SELECT MAX(level) FROM communities`).Scan(&level)
	if err != nil {
		return 0, err
	}
	if !level.Valid {
		return -1, nil
	}
	return int(level.Int64), nil
}

func (s *communityStore) ClearCommunities() error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM community_members`); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM communities`)
		return err
	})
}
