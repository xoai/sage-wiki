package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// P3-6: functional supersession — Postgres twin of
// internal/ontology/temporal.go. Semantics must stay byte-identical; only
// placeholder style and the clamp expression differ.

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
			if table == "derived_relations" && !s.b.derivedExists() {
				continue
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
			// Per-row clamp: valid_to = GREATEST(newValidFrom, valid_from+1s).
			// AT TIME ZONE 'UTC' is mandatory — to_char(timestamptz) renders
			// in the session TimeZone GUC otherwise, mislabeling local time
			// as Z (spec i4). COLLATE "C" makes the text max
			// collation-independent. Empty valid_from → newValidFrom; garbage
			// valid_from fails loudly on the cast (by design, spec i5).
			// Placeholders RESTART at $1: the UPDATE is a separate statement
			// from the SELECT above, so its bind positions are independent.
			// uargs = [newValidFrom($1), newValidFrom($2), invalidatedBy($3), ids($4…)].
			clamp := `CASE WHEN COALESCE(valid_from,'')='' THEN $1` +
				` ELSE GREATEST($2 COLLATE "C", to_char((valid_from::timestamptz + interval '1 second') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') COLLATE "C") END`
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
	return invalidated, nil
}
