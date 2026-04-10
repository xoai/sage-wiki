package ontology

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
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
type Entity struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Definition  string `json:"definition,omitempty"`
	ArticlePath string `json:"article_path,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Relation represents a typed, directed edge between entities.
type Relation struct {
	ID        string `json:"id"`
	SourceID  string `json:"source_id"`
	TargetID  string `json:"target_id"`
	Relation  string `json:"relation"`
	CreatedAt string `json:"created_at"`
}

// Direction for graph traversal.
type Direction int

const (
	Outbound Direction = iota
	Inbound
	Both
)

// TraverseOpts configures graph traversal.
type TraverseOpts struct {
	Direction    Direction
	RelationType string // optional filter
	MaxDepth     int    // 1-5, default 1
}

// Store manages ontology entities and relations.
type Store struct {
	db             *storage.DB
	validRelations map[string]bool
}

// NewStore creates an ontology store with application-layer relation validation.
// validRelations lists the allowed relation type names. If nil, all types are accepted.
func NewStore(db *storage.DB, validRelations []string) *Store {
	s := &Store{db: db}
	if validRelations != nil {
		s.validRelations = make(map[string]bool, len(validRelations))
		for _, r := range validRelations {
			s.validRelations[r] = true
		}
	}
	return s
}

// AddEntity creates a new entity.
func (s *Store) AddEntity(e Entity) error {
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
			   name=excluded.name, definition=excluded.definition,
			   article_path=excluded.article_path, updated_at=excluded.updated_at`,
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
		`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), created_at, updated_at
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
			`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), created_at, updated_at
			 FROM entities WHERE type=? ORDER BY name`, entityType,
		)
	} else {
		rows, err = s.db.ReadDB().Query(
			`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,''), created_at, updated_at
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

// DeleteEntity removes an entity and its relations (via CASCADE).
func (s *Store) DeleteEntity(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entities WHERE id=?", id)
		return err
	})
}

// AddRelation creates a typed edge between two entities.
// Returns error on self-loop. Uses upsert semantics.
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
			`INSERT INTO relations (id, source_id, target_id, relation, created_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(source_id, target_id, relation) DO NOTHING`,
			r.ID, r.SourceID, r.TargetID, r.Relation, r.CreatedAt,
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
		query = "SELECT id, source_id, target_id, relation, created_at FROM relations WHERE source_id=?"
		args = []any{entityID}
	case Inbound:
		query = "SELECT id, source_id, target_id, relation, created_at FROM relations WHERE target_id=?"
		args = []any{entityID}
	case Both:
		query = "SELECT id, source_id, target_id, relation, created_at FROM relations WHERE source_id=? OR target_id=?"
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

	var relations []Relation
	for rows.Next() {
		var r Relation
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Relation, &r.CreatedAt); err != nil {
			return nil, err
		}
		relations = append(relations, r)
	}
	return relations, rows.Err()
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
