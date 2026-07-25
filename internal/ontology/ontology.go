package ontology

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// Entity types
const (
	TypeConcept   = "concept"
	TypeTechnique = "technique"
	TypeSource    = "source"
	TypeClaim     = "claim"
	TypeArtifact  = "artifact"
)

// Relation types
const (
	RelImplements     = "implements"
	RelExtends        = "extends"
	RelOptimizes      = "optimizes"
	RelContradicts    = "contradicts"
	RelCites          = "cites"
	RelPrerequisiteOf = "prerequisite_of"
	RelTradesOff      = "trades_off"
	RelDerivedFrom    = "derived_from"
)

// Entity represents an ontology entity.
type Entity = store.Entity

// Relation represents a typed, directed edge between entities.
type Relation = store.Relation

// Direction for graph traversal.
type Direction = store.Direction

const (
	Outbound = store.Outbound
	Inbound  = store.Inbound
	Both     = store.Both
)

// TraverseOpts configures graph traversal.
type TraverseOpts = store.TraverseOpts

// relationCols is the single relation column list for every read path (P3-1).
// It was seven copies before; the new evidence/provenance columns made keeping
// them in sync by hand a matter of time. COALESCE so pre-V10 rows — where the
// six added columns are NULL — read back as zero values rather than failing the
// scan. Every SELECT using it must scan via scanRelation/scanRelations.
const relationCols = `COALESCE(id,''), source_id, target_id, relation, COALESCE(created_at,''),
	COALESCE(evidence,''), COALESCE(confidence,0), COALESCE(source_doc,''),
	COALESCE(valid_from,''), COALESCE(valid_to,''), COALESCE(invalidated_by,'')`

// Store manages ontology entities and relations.
type Store struct {
	db               store.DBHandle
	validRelations   map[string]bool
	validEntityTypes map[string]bool
}

// NewStore creates an ontology store with application-layer type validation.
// validRelations lists the allowed relation type names. If nil, all types are accepted.
// validEntityTypes lists the allowed entity type names. If nil, all types are accepted.
func NewStore(db store.DBHandle, validRelations []string, validEntityTypes []string) *Store {
	s := &Store{db: db}
	if validRelations != nil {
		s.validRelations = make(map[string]bool, len(validRelations))
		for _, r := range validRelations {
			s.validRelations[r] = true
		}
	}
	if validEntityTypes != nil {
		s.validEntityTypes = make(map[string]bool, len(validEntityTypes))
		for _, t := range validEntityTypes {
			s.validEntityTypes[t] = true
		}
	}
	return s
}

// IsValidType returns true if the given type is in the valid entity types list.
// When no validation list is configured, all types are accepted.
func (s *Store) IsValidType(t string) bool {
	if s.validEntityTypes == nil {
		return true
	}
	return s.validEntityTypes[t]
}

// AddEntity creates a new entity, or updates one that already exists.
//
// Two upsert rules (P3-1):
//
//  1. An empty — or, on Postgres, NULL — incoming name/definition/article_path
//     never clobbers a stored value. Pass 3 re-asserts concept entities without
//     a Definition, which used to erase the descriptions Pass 2 wrote; those
//     descriptions are what entity resolution (P3-3) disambiguates with. The
//     COALESCE is load-bearing on Postgres, where "" binds as NULL.
//  2. type is written unconditionally, so a wrong type stays correctable. SQLite
//     had no type clause at all before P3-1, which made a wrong type permanent
//     (UpdateEntity omits type on both backends). Guarding it instead — treating
//     "concept" as "no information asserted" — would have made an explicit
//     `ontology add --entity-type concept` a silent no-op that still reported
//     success. Callers that index an already-written article derive the type
//     from its frontmatter rather than hard-coding it (ArticleEntityType).
func (s *Store) AddEntity(e Entity) error {
	if s.validEntityTypes != nil && !s.validEntityTypes[e.Type] {
		return fmt.Errorf("ontology: unknown entity type %q", e.Type)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = now
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 			 ON CONFLICT(id) DO UPDATE SET
				   type=excluded.type,
				   name         = CASE WHEN COALESCE(excluded.name,'')         = '' THEN entities.name         ELSE excluded.name         END,
				   definition   = CASE WHEN COALESCE(excluded.definition,'')   = '' THEN entities.definition   ELSE excluded.definition   END,
				   article_path = CASE WHEN COALESCE(excluded.article_path,'') = '' THEN entities.article_path ELSE excluded.article_path END,
				   updated_at=excluded.updated_at`,
			e.ID, e.Type, e.Name, e.Definition, e.ArticlePath, e.CreatedAt, e.UpdatedAt,
		)
		return err
	})
}

// UpdateEntity updates an existing entity.
func (s *Store) UpdateEntity(e Entity) error {
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE entities SET name=?, definition=?, article_path=?, updated_at=? WHERE id=?`,
			e.Name, e.Definition, e.ArticlePath, e.UpdatedAt, e.ID,
		)
		return err
	})
}

// GetEntity retrieves an entity by ID.
func (s *Store) GetEntity(id string) (*Entity, error) {
	row := s.db.ReadDB().QueryRow(
		`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM entities WHERE id=?`, id,
	)
	var e Entity
	if err := row.Scan(&e.ID, &e.Type, &e.Name, &e.Definition, &e.ArticlePath, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// ListEntities returns all entities of a given type, or all if entityType is empty.
func (s *Store) ListEntities(entityType string) ([]Entity, error) {
	var rows *sql.Rows
	var err error
	if entityType != "" {
		rows, err = s.db.ReadDB().Query(
			`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), COALESCE(created_at,''), COALESCE(updated_at,'')
			 FROM entities WHERE type=? ORDER BY name`, entityType,
		)
	} else {
		rows, err = s.db.ReadDB().Query(
			`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), COALESCE(created_at,''), COALESCE(updated_at,'')
			 FROM entities ORDER BY name`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Definition, &e.ArticlePath, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

// ListRelations returns relations filtered by type, up to limit results.
func (s *Store) ListRelations(relationType string, limit int) ([]Relation, error) {
	var rows *sql.Rows
	var err error
	if relationType != "" {
		rows, err = s.db.ReadDB().Query(
			`SELECT `+relationCols+` FROM relations WHERE relation=? ORDER BY created_at DESC LIMIT ?`,
			relationType, limit,
		)
	} else {
		rows, err = s.db.ReadDB().Query(
			`SELECT `+relationCols+` FROM relations ORDER BY created_at DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

// DeleteEntity removes an entity and its relations (via CASCADE).
func (s *Store) DeleteEntity(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entities WHERE id=?", id)
		return err
	})
}

// AddRelation creates a typed edge between two entities.
// Returns error on self-loop.
//
// Re-assertion (P3-1): an existing edge is updated ONLY when the incoming
// confidence is strictly higher than the stored one. created_at is never in the
// SET list, so the earliest assertion's timestamp survives, and the stored id
// is kept. The three temporal columns are insert-only until P3-6 — a
// re-assertion leaves them untouched, so P3-6 must add them to the SET list
// when it starts writing them.
//
// The WHERE clause is the back-compat proof: every caller that predates P3-1
// passes Confidence 0, so `0 > COALESCE(stored, 0)` is false and the statement
// is a no-op — bit-identical to the DO NOTHING it replaces. It also means the
// Pass-3 keyword extractor, which re-asserts the same (source, target,
// relation) on every compile, can never erase an LLM-extracted edge's
// evidence.
func (s *Store) AddRelation(r Relation) error {
	if r.SourceID == r.TargetID {
		return fmt.Errorf("ontology: self-loops not allowed (entity %q)", r.SourceID)
	}
	if s.validRelations != nil && !s.validRelations[r.Relation] {
		return fmt.Errorf("ontology: unknown relation type %q", r.Relation)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO relations (id, source_id, target_id, relation, created_at,
			                        evidence, confidence, source_doc,
			                        valid_from, valid_to, invalidated_by)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(source_id, target_id, relation) DO UPDATE SET
			   evidence   = excluded.evidence,
			   confidence = excluded.confidence,
			   source_doc = excluded.source_doc
			 WHERE excluded.confidence > COALESCE(relations.confidence, 0)`,
			r.ID, r.SourceID, r.TargetID, r.Relation, r.CreatedAt,
			r.Evidence, r.Confidence, r.SourceDoc,
			r.ValidFrom, r.ValidTo, r.InvalidatedBy,
		)
		return err
	})
}

// GetRelations returns relations for an entity in a given direction.
func (s *Store) GetRelations(entityID string, direction Direction, relationType string) ([]Relation, error) {
	var query string
	var args []any

	switch direction {
	case Outbound:
		query = "SELECT " + relationCols + " FROM relations WHERE source_id=?"
		args = []any{entityID}
	case Inbound:
		query = "SELECT " + relationCols + " FROM relations WHERE target_id=?"
		args = []any{entityID}
	case Both:
		// Parenthesized (P3-1/D11): AND binds tighter than OR, so the
		// unparenthesized form made the relationType filter below apply to the
		// target side only — a Both query with a filter returned outbound edges
		// of every type. Postgres already parenthesized; this is the SQLite
		// half of that parity.
		query = "SELECT " + relationCols + " FROM relations WHERE (source_id=? OR target_id=?)"
		args = []any{entityID, entityID}
	}

	if relationType != "" {
		query += " AND relation=?"
		args = append(args, relationType)
	}

	rows, err := s.db.ReadDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

// Traverse performs BFS traversal from an entity, returning connected entities.
func (s *Store) Traverse(entityID string, opts TraverseOpts) ([]Entity, error) {
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 1
	}
	if opts.MaxDepth > 5 {
		opts.MaxDepth = 5
	}

	visited := map[string]bool{entityID: true}
	queue := []string{entityID}
	var result []Entity

	for depth := 0; depth < opts.MaxDepth && len(queue) > 0; depth++ {
		var nextQueue []string
		for _, id := range queue {
			rels, err := s.GetRelations(id, opts.Direction, opts.RelationType)
			if err != nil {
				return nil, err
			}
			for _, r := range rels {
				neighborID := r.TargetID
				if neighborID == id {
					neighborID = r.SourceID
				}
				if visited[neighborID] {
					continue
				}
				visited[neighborID] = true
				nextQueue = append(nextQueue, neighborID)

				entity, err := s.GetEntity(neighborID)
				if err != nil {
					return nil, err
				}
				if entity != nil {
					result = append(result, *entity)
				}
			}
		}
		queue = nextQueue
	}

	return result, nil
}

// DetectCycles performs iterative DFS to find cycles reachable from entityID
// following outbound edges. Returns cycle paths if found.
func (s *Store) DetectCycles(entityID string) ([][]string, error) {
	var cycles [][]string

	type frame struct {
		id   string
		path []string
	}

	stack := []frame{{id: entityID, path: []string{entityID}}}

	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		rels, err := s.GetRelations(f.id, Outbound, "")
		if err != nil {
			return nil, err
		}

		for _, r := range rels {
			if r.TargetID == entityID {
				cycle := append(append([]string{}, f.path...), r.TargetID)
				cycles = append(cycles, cycle)
				continue
			}

			inPath := false
			for _, p := range f.path {
				if p == r.TargetID {
					inPath = true
					break
				}
			}
			if inPath {
				continue
			}

			newPath := append(append([]string{}, f.path...), r.TargetID)
			stack = append(stack, frame{id: r.TargetID, path: newPath})
		}
	}

	return cycles, nil
}

// EntityCount returns the number of entities, optionally filtered by type.
func (s *Store) EntityCount(entityType string) (int, error) {
	var count int
	var err error
	if entityType != "" {
		err = s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM entities WHERE type=?", entityType).Scan(&count)
	} else {
		err = s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM entities").Scan(&count)
	}
	return count, err
}

// RelationCount returns the number of relations.
func (s *Store) RelationCount() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM relations").Scan(&count)
	return count, err
}

// EntityDegree returns the total number of relations (inbound + outbound) for an entity.
func (s *Store) EntityDegree(id string) (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow(
		"SELECT COUNT(*) FROM relations WHERE source_id=? OR target_id=?", id, id,
	).Scan(&count)
	return count, err
}

// EntitiesCiting returns all entities that have a "cites" relation pointing TO targetID.
// This is the reverse lookup: "which concepts cite this source?"
func (s *Store) EntitiesCiting(targetID string) ([]Entity, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT e.id, e.type, e.name, COALESCE(e.definition,''), COALESCE(e.article_path,''), COALESCE(e.created_at,''), COALESCE(e.updated_at,'')
		 FROM entities e
		 JOIN relations r ON r.source_id = e.id
		 WHERE r.target_id=? AND r.relation=?`, targetID, RelCites,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Definition, &e.ArticlePath, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

// CitedBy returns all entities that entityID cites (forward "cites" lookup).
// This answers: "which sources does this concept cite?"
func (s *Store) CitedBy(entityID string) ([]Entity, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT e.id, e.type, e.name, COALESCE(e.definition,''), COALESCE(e.article_path,''), COALESCE(e.created_at,''), COALESCE(e.updated_at,'')
		 FROM entities e
		 JOIN relations r ON r.target_id = e.id
		 WHERE r.source_id=? AND r.relation=?`, entityID, RelCites,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &e.Definition, &e.ArticlePath, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}

// AllRelations returns every relation, fully populated (P2-1: absorbs the web
// graph view's unbounded relations dump — spec §3: full rows, not the
// 3-column handler shape).
func (s *Store) AllRelations() ([]Relation, error) {
	rows, err := s.db.ReadDB().Query(`SELECT ` + relationCols + ` FROM relations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

// RelationsByType returns all relations of one type, fully populated
// (P2-1: absorbs linter's contradicts-edge scan).
func (s *Store) RelationsByType(relationType string) ([]Relation, error) {
	rows, err := s.db.ReadDB().Query(
		`SELECT `+relationCols+` FROM relations WHERE relation=?`, relationType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

// EntityConnectionCounts returns per-entity relation counts, summing both the
// source and target side (P2-1: absorbs web/server.go's UNION ALL query —
// one pass, no N+1). PARITY NOTE: the absorbed query's outer GROUP BY id has
// no SUM aggregate, so dual-side entities report one side's count, not the
// total (latent bug, reproduced byte-for-byte; fix deferred, decisions.md
// 2026-07-21).
func (s *Store) EntityConnectionCounts() (map[string]int, error) {
	rows, err := s.db.ReadDB().Query(`
		SELECT id, cnt FROM (
			SELECT source_id AS id, COUNT(*) AS cnt FROM relations GROUP BY source_id
			UNION ALL
			SELECT target_id AS id, COUNT(*) AS cnt FROM relations GROUP BY target_id
		) GROUP BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var cnt int
		if err := rows.Scan(&id, &cnt); err != nil {
			return nil, err
		}
		counts[id] += cnt
	}
	return counts, rows.Err()
}

// scanRelations is the single scan for every relationCols query. Column order
// here and in relationCols must stay in lockstep.
func scanRelations(rows *sql.Rows) ([]Relation, error) {
	var rels []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(
			&r.ID, &r.SourceID, &r.TargetID, &r.Relation, &r.CreatedAt,
			&r.Evidence, &r.Confidence, &r.SourceDoc,
			&r.ValidFrom, &r.ValidTo, &r.InvalidatedBy,
		); err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}
