package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/store"
)

// P3-6: functional supersession — Postgres twin of
// internal/ontology/temporal.go. Semantics are identical; only the
// placeholder style differs.

// aliasRoot / idForms: see the SQLite twin. Edges are written with
// pre-resolution LLM IDs and LinkAlias rewrites only derived copies, so one
// logical edge exists under multiple ID forms; supersession closes over the
// applied-alias chain root.
func aliasRoot(id string, edges map[string]string) string {
	visited := map[string]bool{id: true}
	cur := id
	for {
		next, ok := edges[cur]
		if !ok || visited[next] {
			return cur
		}
		visited[next] = true
		cur = next
	}
}

func idForms(id string, edges map[string]string) []string {
	root := aliasRoot(id, edges)
	forms := map[string]bool{id: true, root: true}
	for a := range edges {
		if aliasRoot(a, edges) == root {
			forms[a] = true
		}
	}
	out := make([]string, 0, len(forms))
	for f := range forms {
		out = append(out, f)
	}
	return out
}

// pgPlaceholders returns "$n,$n+1,..." and the next free index.
func pgPlaceholders(n, start int) (string, int) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "$%d,", start+i)
	}
	return strings.TrimSuffix(b.String(), ","), start + n
}

// InvalidateFunctional implements store.OntologyStore. See the interface doc.
func (s *ontologyStore) InvalidateFunctional(sourceID, predicate, keepTargetID, newValidFrom, invalidatedBy string) ([]string, error) {
	if !s.temporalEnabled() {
		return nil, nil
	}
	if newValidFrom == "" {
		// Unknown winner date → now; never skip silently (spec i3 Major).
		newValidFrom = asOfString(time.Now())
	}
	aliases, err := s.ListAliases(store.AliasApplied)
	if err != nil {
		return nil, fmt.Errorf("postgres.InvalidateFunctional: list aliases: %w", err)
	}
	edges := make(map[string]string, len(aliases))
	for _, a := range aliases {
		edges[a.Alias] = a.CanonicalID
	}
	sourceForms := idForms(sourceID, edges)
	keepForms := idForms(keepTargetID, edges)

	var invalidated []string
	err = s.b.WriteTx(func(tx *sql.Tx) error {
		n := 1
		predPH, next := pgPlaceholders(1, n)
		srcPH, next := pgPlaceholders(len(sourceForms), next)
		keepPH, _ := pgPlaceholders(len(keepForms), next)

		where := `relation = ` + predPH +
			` AND source_id IN (` + srcPH + `)` +
			` AND target_id NOT IN (` + keepPH + `)` +
			` AND COALESCE(valid_to,'') = ''`
		mkArgs := func() []any {
			args := []any{predicate}
			for _, f := range sourceForms {
				args = append(args, f)
			}
			for _, f := range keepForms {
				args = append(args, f)
			}
			return args
		}

		for _, table := range []string{"relations", "derived_relations"} {
			if table == "derived_relations" && !s.b.derivedExistsFresh() {
				continue // uncached probe — see the SQLite twin
			}
			ids, err := func() ([]string, error) {
				rows, err := tx.Query(`SELECT id FROM `+table+` WHERE `+where, mkArgs()...)
				if err != nil {
					return nil, fmt.Errorf("postgres.InvalidateFunctional: scan %s: %w", table, err)
				}
				defer rows.Close()
				var ids []string
				for rows.Next() {
					var id sql.NullString
					if err := rows.Scan(&id); err != nil {
						return nil, err
					}
					if id.Valid {
						ids = append(ids, id.String)
					}
				}
				return ids, rows.Err()
			}()
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				continue
			}
			// Per-row rule: valid_to = GREATEST(newValidFrom, valid_from) —
			// plain text max, no timestamptz arithmetic (the earlier +1s
			// clamp left a 1-second overlap window — Gate-8 QA; see the
			// SQLite twin for the full rationale). Equality = retroactive
			// win: the loser is live at no T. COLLATE "C" keeps the max
			// collation-independent. Placeholders RESTART at $1: the UPDATE
			// is a separate statement from the SELECT above.
			// uargs = [newValidFrom($1), newValidFrom($2), invalidatedBy($3), ids($4…)].
			clamp := `CASE WHEN COALESCE(valid_from,'')='' THEN $1` +
				` ELSE GREATEST($2 COLLATE "C", valid_from COLLATE "C") END`
			idPH, _ := pgPlaceholders(len(ids), 4)
			upd := `UPDATE ` + table + ` SET valid_to = ` + clamp +
				`, invalidated_by = $3 WHERE id IN (` + idPH + `)`
			uargs := []any{newValidFrom, newValidFrom, invalidatedBy}
			for _, id := range ids {
				uargs = append(uargs, id)
			}
			if _, err := tx.Exec(upd, uargs...); err != nil {
				return fmt.Errorf("postgres.InvalidateFunctional: update %s: %w", table, err)
			}
			invalidated = append(invalidated, ids...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// SPEC-07: report every invalidated edge (winner's start = effective
	// invalidation boundary; per-row valid_from not read back).
	if validTo := parseValidStamp(newValidFrom); len(invalidated) > 0 {
		for _, id := range invalidated {
			ontology.EmitEdgeInvalidated(s.sink, id, invalidatedBy, nil, validTo)
		}
	}
	return invalidated, nil
}

// parseValidStamp parses an RFC3339 validity stamp; nil when empty or
// unparseable (unknown windows are reported as unknown, never faked).
func parseValidStamp(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
