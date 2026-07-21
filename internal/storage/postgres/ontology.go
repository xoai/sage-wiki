package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

type ontologyStore struct{ b *backend }

var _ store.OntologyStore = (*ontologyStore)(nil)

func (s *ontologyStore) IsValidType(t string) bool {
	switch t {
	case "concept", "technique", "source", "claim", "artifact":
		return true
	}
	// Custom types are allowed (V4 dropped the CHECK constraint) — the
	// sqlite store consults its constructor lists; postgres accepts all
	// non-empty types (validation stays a construction-time concern, spec §3).
	return t != ""
}

func (s *ontologyStore) AddEntity(e store.Entity) error {
	if e.CreatedAt == "" {
		e.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		e.UpdatedAt = e.CreatedAt
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET
				type=excluded.type, name=excluded.name, definition=excluded.definition,
				article_path=excluded.article_path, updated_at=excluded.updated_at`,
			e.ID, e.Type, e.Name, nullStr(e.Definition), nullStr(e.ArticlePath),
			nullRFC(e.CreatedAt), nullRFC(e.UpdatedAt))
		return err
	})
}

func (s *ontologyStore) UpdateEntity(e store.Entity) error {
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE entities SET name=$2, definition=$3, article_path=$4, updated_at=$5 WHERE id=$1",
			e.ID, e.Name, nullStr(e.Definition), nullStr(e.ArticlePath), nullRFC(e.UpdatedAt))
		return err
	})
}

func scanEntity(row interface{ Scan(...any) error }) (*store.Entity, error) {
	var e store.Entity
	var def, ap sql.NullString
	var ca, ua *time.Time
	if err := row.Scan(&e.ID, &e.Type, &e.Name, &def, &ap, &ca, &ua); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	e.Definition, e.ArticlePath = def.String, ap.String
	e.CreatedAt = scanNullRFC(ca)
	e.UpdatedAt = scanNullRFC(ua)
	return &e, nil
}

func (s *ontologyStore) GetEntity(id string) (*store.Entity, error) {
	return scanEntity(s.b.pool.QueryRow(
		"SELECT id, type, name, definition, article_path, created_at, updated_at FROM entities WHERE id=$1", id))
}

func (s *ontologyStore) ListEntities(entityType string) ([]store.Entity, error) {
	var rows *sql.Rows
	var err error
	if entityType != "" {
		rows, err = s.b.pool.Query(
			"SELECT id, type, name, definition, article_path, created_at, updated_at FROM entities WHERE type=$1 ORDER BY name",
			entityType)
	} else {
		rows, err = s.b.pool.Query(
			"SELECT id, type, name, definition, article_path, created_at, updated_at FROM entities ORDER BY name")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *ontologyStore) AddRelation(r store.Relation) error {
	if r.SourceID == r.TargetID {
		return fmt.Errorf("ontology: self-loops not allowed (entity %q)", r.SourceID)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO relations (id, source_id, target_id, relation, created_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (source_id, target_id, relation) DO NOTHING`,
			r.ID, r.SourceID, r.TargetID, r.Relation, nullRFC(r.CreatedAt))
		return err
	})
}

const relationCols = "COALESCE(id,''), source_id, target_id, relation, created_at"

func scanRelations(rows *sql.Rows) ([]store.Relation, error) {
	var out []store.Relation
	for rows.Next() {
		var r store.Relation
		var ca *time.Time
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Relation, &ca); err != nil {
			return nil, err
		}
		r.CreatedAt = scanNullRFC(ca)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *ontologyStore) ListRelations(relationType string, limit int) ([]store.Relation, error) {
	var rows *sql.Rows
	var err error
	if relationType != "" {
		rows, err = s.b.pool.Query(
			"SELECT "+relationCols+" FROM relations WHERE relation=$1 ORDER BY created_at DESC LIMIT $2",
			relationType, limit)
	} else {
		rows, err = s.b.pool.Query(
			"SELECT "+relationCols+" FROM relations ORDER BY created_at DESC LIMIT $1", limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) AllRelations() ([]store.Relation, error) {
	rows, err := s.b.pool.Query("SELECT " + relationCols + " FROM relations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) RelationsByType(relationType string) ([]store.Relation, error) {
	rows, err := s.b.pool.Query(
		"SELECT "+relationCols+" FROM relations WHERE relation=$1", relationType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) EntityConnectionCounts() (map[string]int, error) {
	// PARITY: the absorbed query's outer GROUP BY id has no SUM aggregate —
	// on postgres the equivalent bare-column pick is not valid SQL, so this
	// uses the same UNION ALL shape and sums Go-side... NO — parity note
	// (decisions.md 2026-07-21): sqlite picks ONE side's count arbitrarily.
	// Postgres cannot express "arbitrary bare column" portably, so we use
	// MAX(cnt) which matches sqlite's typical first-row behavior for the
	// dual-side case in the fixtures the web view was built against.
	rows, err := s.b.pool.Query(`
		SELECT id, MAX(cnt) FROM (
			SELECT source_id AS id, COUNT(*) AS cnt FROM relations GROUP BY source_id
			UNION ALL
			SELECT target_id AS id, COUNT(*) AS cnt FROM relations GROUP BY target_id
		) x GROUP BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var cnt int
		if err := rows.Scan(&id, &cnt); err != nil {
			return nil, err
		}
		counts[id] = cnt
	}
	return counts, rows.Err()
}

func (s *ontologyStore) DeleteEntity(id string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entities WHERE id=$1", id)
		return err
	})
}

func (s *ontologyStore) GetRelations(entityID string, direction store.Direction, relationType string) ([]store.Relation, error) {
	var conds []string
	var args []any
	n := 1
	switch direction {
	case store.Outbound:
		conds = append(conds, fmt.Sprintf("source_id=$%d", n))
		n++
		args = append(args, entityID)
	case store.Inbound:
		conds = append(conds, fmt.Sprintf("target_id=$%d", n))
		n++
		args = append(args, entityID)
	default:
		conds = append(conds, fmt.Sprintf("(source_id=$%d OR target_id=$%d)", n, n))
		n++
		args = append(args, entityID)
	}
	if relationType != "" {
		conds = append(conds, fmt.Sprintf("relation=$%d", n))
		args = append(args, relationType)
	}
	rows, err := s.b.pool.Query(
		"SELECT "+relationCols+" FROM relations WHERE "+strings.Join(conds, " AND ")+" ORDER BY created_at DESC",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) Traverse(entityID string, opts store.TraverseOpts) ([]store.Entity, error) {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if maxDepth > 5 {
		maxDepth = 5
	}
	visited := map[string]bool{entityID: true}
	frontier := []string{entityID}
	var out []store.Entity
	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			var rels []store.Relation
			var err error
			switch opts.Direction {
			case store.Outbound:
				rels, err = s.GetRelations(id, store.Outbound, opts.RelationType)
			case store.Inbound:
				rels, err = s.GetRelations(id, store.Inbound, opts.RelationType)
			default:
				rels, err = s.GetRelations(id, store.Both, opts.RelationType)
			}
			if err != nil {
				return nil, err
			}
			for _, r := range rels {
				other := r.TargetID
				if r.TargetID == id {
					other = r.SourceID
				}
				if visited[other] {
					continue
				}
				visited[other] = true
				next = append(next, other)
				e, err := s.GetEntity(other)
				if err == nil && e != nil {
					out = append(out, *e)
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func (s *ontologyStore) DetectCycles(entityID string) ([][]string, error) {
	var cycles [][]string
	var visit func(id string, path []string, seen map[string]bool, depth int) error
	visit = func(id string, path []string, seen map[string]bool, depth int) error {
		if depth > 10 {
			return nil
		}
		rels, err := s.GetRelations(id, store.Outbound, "")
		if err != nil {
			return err
		}
		for _, r := range rels {
			if r.TargetID == entityID {
				cycles = append(cycles, append(append([]string{}, path...), id, r.TargetID))
				continue
			}
			if seen[r.TargetID] {
				continue
			}
			seen[r.TargetID] = true
			if err := visit(r.TargetID, append(path, id), seen, depth+1); err != nil {
				return err
			}
			delete(seen, r.TargetID)
		}
		return nil
	}
	return cycles, visit(entityID, nil, map[string]bool{entityID: true}, 0)
}

func (s *ontologyStore) EntityCount(entityType string) (int, error) {
	var n int
	var err error
	if entityType != "" {
		err = s.b.pool.QueryRow("SELECT COUNT(*) FROM entities WHERE type=$1", entityType).Scan(&n)
	} else {
		err = s.b.pool.QueryRow("SELECT COUNT(*) FROM entities").Scan(&n)
	}
	return n, err
}

func (s *ontologyStore) RelationCount() (int, error) {
	var n int
	err := s.b.pool.QueryRow("SELECT COUNT(*) FROM relations").Scan(&n)
	return n, err
}

func (s *ontologyStore) EntityDegree(id string) (int, error) {
	var n int
	err := s.b.pool.QueryRow(
		"SELECT (SELECT COUNT(*) FROM relations WHERE source_id=$1) + (SELECT COUNT(*) FROM relations WHERE target_id=$1)", id).Scan(&n)
	return n, err
}

func (s *ontologyStore) EntitiesCiting(targetID string) ([]store.Entity, error) {
	rows, err := s.b.pool.Query(`
		SELECT e.id, e.type, e.name, e.definition, e.article_path, e.created_at, e.updated_at
		FROM entities e JOIN relations r ON r.source_id = e.id
		WHERE r.target_id=$1 ORDER BY e.name`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (s *ontologyStore) CitedBy(entityID string) ([]store.Entity, error) {
	rows, err := s.b.pool.Query(`
		SELECT e.id, e.type, e.name, e.definition, e.article_path, e.created_at, e.updated_at
		FROM entities e JOIN relations r ON r.target_id = e.id
		WHERE r.source_id=$1 ORDER BY e.name`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
