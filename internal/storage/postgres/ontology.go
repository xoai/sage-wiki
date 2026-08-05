package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/pkg/events"
)

type ontologyStore struct {
	b *backend
	// sink receives the edge lifecycle events (SPEC-07); nil = no events.
	// A FRESH store is returned per Backend.Ontology() call, so the
	// injection sites set the sink on every acquisition.
	sink events.Sink
}

// SetEventSink installs the event sink (SPEC-07 narrow setter,
// type-asserted by the injection sites).
func (s *ontologyStore) SetEventSink(sink events.Sink) {
	s.sink = events.NilSafe(sink) // typed-nil guard
}

// temporalEnabled resolves the P3-6 gate from OpenOptions (nil = enabled).
func (s *ontologyStore) temporalEnabled() bool {
	if s.b.opts.TemporalEnabled == nil {
		return true
	}
	return *s.b.opts.TemporalEnabled
}

// liveAtPredicate is the Postgres twin of ontology.liveAtPredicate (P3-6):
// "edge live at $n/$n+1" over COALESCE'd TEXT columns. Writer-produced values
// are RFC3339 UTC, so TEXT comparison is chronological; COALESCE maps legacy
// NULL (nullStr binds "" → NULL) to "unset". valid_to is strict.
func liveAtPredicate(alias string, n int) (string, int) {
	vf := alias + "valid_from"
	vt := alias + "valid_to"
	return fmt.Sprintf("(COALESCE(%s,'')='' OR COALESCE(%s,'')<=$%d) AND (COALESCE(%s,'')='' OR COALESCE(%s,'')>$%d)",
		vf, vf, n, vt, vt, n+1), n + 2
}

// asOfString matches the SQLite twin: writers produce RFC3339 UTC.
func asOfString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

var _ store.OntologyStore = (*ontologyStore)(nil)

func (s *ontologyStore) validSets() (rels, types map[string]bool) {
	rels = map[string]bool{}
	for _, r := range s.b.opts.ValidRelations {
		rels[r] = true
	}
	types = map[string]bool{}
	for _, t := range s.b.opts.ValidEntityTypes {
		types[t] = true
	}
	return rels, types
}

func (s *ontologyStore) IsValidType(t string) bool {
	_, types := s.validSets()
	if len(types) == 0 {
		return t != ""
	}
	return types[t]
}

func (s *ontologyStore) AddEntity(e store.Entity) error {
	// Two INDEPENDENT defaults, mirroring the sqlite store: coupling them meant
	// a caller supplying CreatedAt but not UpdatedAt bound nullRFC("") → NULL,
	// and the unconditional SET below wrote that NULL over a stored timestamp.
	now := time.Now().UTC().Format(time.RFC3339)
	if e.CreatedAt == "" {
		e.CreatedAt = now
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = now
	}
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (id) DO UPDATE SET
				type=excluded.type,
				name         = CASE WHEN COALESCE(excluded.name,'')         = '' THEN entities.name         ELSE excluded.name         END,
				definition   = CASE WHEN COALESCE(excluded.definition,'')   = '' THEN entities.definition   ELSE excluded.definition   END,
				article_path = CASE WHEN COALESCE(excluded.article_path,'') = '' THEN entities.article_path ELSE excluded.article_path END,
				updated_at=excluded.updated_at`,
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
	if rels, _ := s.validSets(); len(rels) > 0 && !rels[r.Relation] {
		return fmt.Errorf("ontology: unknown relation type %q", r.Relation)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	var changed bool
	err := s.b.WriteTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			INSERT INTO relations (id, source_id, target_id, relation, created_at,
			                       evidence, confidence, source_doc,
			                       valid_from, valid_to, invalidated_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (source_id, target_id, relation) DO UPDATE SET
			  evidence   = excluded.evidence,
			  confidence = excluded.confidence,
			  source_doc = excluded.source_doc,
			  valid_from = CASE WHEN COALESCE(relations.valid_from,'') = ''
			                    THEN excluded.valid_from ELSE relations.valid_from END
			WHERE excluded.confidence > COALESCE(relations.confidence, 0)`,
			r.ID, r.SourceID, r.TargetID, r.Relation, nullRFC(r.CreatedAt),
			nullStr(r.Evidence), r.Confidence, nullStr(r.SourceDoc),
			nullStr(r.ValidFrom), nullStr(r.ValidTo), nullStr(r.InvalidatedBy))
		if err != nil {
			return err
		}
		// SPEC-07: no-op re-assertions touch zero rows — no edge_added
		// (parity with the sqlite store).
		if n, rerr := res.RowsAffected(); rerr == nil {
			changed = n > 0
		} else {
			changed = true // unknown → report rather than under-report
		}
		return nil
	})
	if err == nil && changed {
		ontology.EmitEdgeAdded(s.sink, r.ID, r.Relation, r.SourceID, r.TargetID, parseValidStamp(r.ValidFrom))
	}
	return err
}

// relationCols COALESCEs the P3-1 columns so pre-v3 rows — where they are
// NULL — read back as zero values instead of failing the scan. Only ever
// interpolated as "SELECT " + relationCols + " FROM relations", so expressions
// are safe here. Column order must match scanRelations.
const relationCols = `id, source_id, target_id, relation, created_at,
	COALESCE(evidence,''), COALESCE(confidence,0), COALESCE(source_doc,''),
	COALESCE(valid_from,''), COALESCE(valid_to,''), COALESCE(invalidated_by,'')`

func scanRelations(rows *sql.Rows) ([]store.Relation, error) {
	var out []store.Relation
	for rows.Next() {
		var r store.Relation
		var ca *time.Time
		if err := rows.Scan(
			&r.ID, &r.SourceID, &r.TargetID, &r.Relation, &ca,
			&r.Evidence, &r.Confidence, &r.SourceDoc,
			&r.ValidFrom, &r.ValidTo, &r.InvalidatedBy,
		); err != nil {
			return nil, err
		}
		r.CreatedAt = scanNullRFC(ca)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *ontologyStore) ListRelations(relationType string, limit int) ([]store.Relation, error) {
	// Negative limit = unlimited (sqlite LIMIT -1 parity; postgres errors
	// on negative LIMIT, so the clause is omitted instead).
	limitFrag := ""
	args := []any{}
	if relationType != "" {
		args = append(args, relationType)
	}
	if limit >= 0 {
		limitFrag = fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, limit)
	}
	// ORDER BY and LIMIT apply to the whole union, so they follow it.
	var base, dpred string
	if relationType != "" {
		base = "SELECT " + relationCols + " FROM relations WHERE relation=$1"
		dpred = "d.relation=$1"
	} else {
		base = "SELECT " + relationCols + " FROM relations"
		dpred = "TRUE"
	}
	rows, err := s.b.pool.Query(
		s.unionIfDerived(base, dpred)+" ORDER BY created_at DESC"+limitFrag, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) AllRelations() ([]store.Relation, error) {
	rows, err := s.b.pool.Query(s.unionIfDerived("SELECT "+relationCols+" FROM relations", "TRUE"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) RelationsByType(relationType string) ([]store.Relation, error) {
	base, dcond := "SELECT "+relationCols+" FROM relations WHERE relation=$1", "d.relation=$1"
	args := []any{relationType}
	if s.temporalEnabled() {
		// Live-at-now (P3-6) — see the SQLite twin.
		asOfStr := asOfString(time.Now())
		pred, _ := liveAtPredicate("", 2)
		dpred, _ := liveAtPredicate("d.", 2)
		base += " AND " + pred
		dcond += " AND " + dpred
		args = append(args, asOfStr, asOfStr)
	}
	rows, err := s.b.pool.Query(
		s.unionIfDerived(base, dcond),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRelations(rows)
}

func (s *ontologyStore) EntityConnectionCounts() (map[string]int, error) {
	// PARITY preserved exactly, MAX(cnt) included — see the SQLite twin. Only
	// the source changes; fixing the quirk here would be an unrelated
	// behaviour change (spec §10 files it, deliberately unfixed).
	src := s.endpointSource("", "TRUE")
	rows, err := s.b.pool.Query(`
		SELECT id, MAX(cnt) FROM (
			SELECT source_id AS id, COUNT(*) AS cnt FROM (` + src + `) a GROUP BY source_id
			UNION ALL
			SELECT target_id AS id, COUNT(*) AS cnt FROM (` + src + `) b GROUP BY target_id
		) x GROUP BY id`)
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

func (s *ontologyStore) DeleteEntity(id string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entities WHERE id=$1", id)
		return err
	})
}

func (s *ontologyStore) GetRelations(entityID string, direction store.Direction, relationType string) ([]store.Relation, error) {
	return s.GetRelationsAt(entityID, direction, relationType, time.Now())
}

func (s *ontologyStore) GetRelationsAt(entityID string, direction store.Direction, relationType string, asOf time.Time) ([]store.Relation, error) {
	if asOf.IsZero() {
		asOf = time.Now()
	}
	var conds, dconds []string
	var args []any
	n := 1
	switch direction {
	case store.Outbound:
		conds = append(conds, fmt.Sprintf("source_id=$%d", n))
		dconds = append(dconds, fmt.Sprintf("d.source_id=$%d", n))
		n++
		args = append(args, entityID)
	case store.Inbound:
		conds = append(conds, fmt.Sprintf("target_id=$%d", n))
		dconds = append(dconds, fmt.Sprintf("d.target_id=$%d", n))
		n++
		args = append(args, entityID)
	default:
		conds = append(conds, fmt.Sprintf("(source_id=$%d OR target_id=$%d)", n, n))
		dconds = append(dconds, fmt.Sprintf("(d.source_id=$%d OR d.target_id=$%d)", n, n))
		n++
		args = append(args, entityID)
	}
	if relationType != "" {
		conds = append(conds, fmt.Sprintf("relation=$%d", n))
		dconds = append(dconds, fmt.Sprintf("d.relation=$%d", n))
		n++
		args = append(args, relationType)
	}
	if s.temporalEnabled() {
		asOfStr := asOfString(asOf)
		var pred, dpred string
		pred, _ = liveAtPredicate("", n)
		dpred, _ = liveAtPredicate("d.", n)
		conds = append(conds, pred)
		dconds = append(dconds, dpred)
		args = append(args, asOfStr, asOfStr)
	}
	// ORDER BY moves outside the union so it sorts the whole result, not just
	// the first arm. $N placeholders are reusable, so args are unchanged.
	q := s.unionIfDerived(
		"SELECT "+relationCols+" FROM relations WHERE "+strings.Join(conds, " AND "),
		strings.Join(dconds, " AND "),
	) + " ORDER BY created_at DESC"
	rows, err := s.b.pool.Query(q, args...)
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
	overCap := false
	for depth := 0; depth < maxDepth && len(frontier) > 0 && !overCap; depth++ {
		var next []string
		for _, id := range frontier {
			var rels []store.Relation
			var err error
			switch opts.Direction {
			case store.Outbound:
				rels, err = s.GetRelationsAt(id, store.Outbound, opts.RelationType, opts.AsOf)
			case store.Inbound:
				rels, err = s.GetRelationsAt(id, store.Inbound, opts.RelationType, opts.AsOf)
			default:
				rels, err = s.GetRelationsAt(id, store.Both, opts.RelationType, opts.AsOf)
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
				if opts.MaxNodes > 0 && len(visited) > opts.MaxNodes {
					overCap = true
					break
				}
				next = append(next, other)
				e, err := s.GetEntity(other)
				if err == nil && e != nil {
					out = append(out, *e)
				}
			}
			if overCap {
				break
			}
		}
		frontier = next
	}
	if overCap {
		return out, limits.ErrTraversalTooWide
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
	// Unioned — see the SQLite twin for why this is not a pass-through.
	var n int
	err := s.b.pool.QueryRow(
		`SELECT COUNT(*) FROM (` + s.endpointSource("", "TRUE") + `) x`).Scan(&n)
	return n, err
}

func (s *ontologyStore) EntityDegree(id string) (int, error) {
	var n int
	src := s.endpointSource("source_id=$1 OR target_id=$1", "(d.source_id=$1 OR d.target_id=$1)")
	err := s.b.pool.QueryRow(
		"SELECT COUNT(*) FROM ("+src+") x WHERE x.source_id=$1 OR x.target_id=$1", id).Scan(&n)
	return n, err
}

func (s *ontologyStore) EntitiesCiting(targetID string) ([]store.Entity, error) {
	// NB: no relation filter here, unlike the SQLite twin. That divergence
	// predates decision-035 and is deliberately left alone (spec §10).
	//
	// BEHAVIOUR CHANGE, declared: this was a JOIN, and a JOIN emitted one row
	// per matching edge — so an entity with two relation types to the target
	// appeared twice. IN (...) returns it once. That is the answer SQLite
	// already gives (it filters on RelCites), so this narrows the divergence
	// rather than widening it, but it is a change and not merely "the same
	// query over a different source".
	inner := "SELECT source_id FROM relations WHERE target_id=$1"
	if s.b.derivedExists() {
		inner += "\nUNION ALL\nSELECT d.source_id FROM derived_relations d WHERE d.target_id=$1" +
			derivedNotShadowed
	}
	rows, err := s.b.pool.Query(`
		SELECT e.id, e.type, e.name, e.definition, e.article_path, e.created_at, e.updated_at
		FROM entities e WHERE e.id IN (`+inner+`) ORDER BY e.name`, targetID)
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
	// Same declared behaviour change as EntitiesCiting: JOIN emitted duplicates
	// across relation types, IN (...) does not.
	inner := "SELECT target_id FROM relations WHERE source_id=$1"
	if s.b.derivedExists() {
		inner += "\nUNION ALL\nSELECT d.target_id FROM derived_relations d WHERE d.source_id=$1" +
			derivedNotShadowed
	}
	rows, err := s.b.pool.Query(`
		SELECT e.id, e.type, e.name, e.definition, e.article_path, e.created_at, e.updated_at
		FROM entities e WHERE e.id IN (`+inner+`) ORDER BY e.name`, entityID)
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
