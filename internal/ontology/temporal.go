package ontology

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
	"sort"
)

// P3-6: bi-temporal edge validity.
//
// An edge is LIVE AT T iff (valid_from unset OR valid_from <= T) AND
// (valid_to unset OR valid_to > T). All writer-produced values are RFC3339
// UTC, so TEXT comparison is chronological; COALESCE maps both legacy NULL
// (Postgres binds "" to NULL) and SQLite '' to "unset". valid_to is strict:
// the edge stopped being true AT valid_to.

// liveAtPredicate returns the SQL fragment for "edge live at ?", with columns
// qualified by alias ("" for relations, "d." for the derived arm). One
// definition for every read path so the two union arms can never diverge.
func liveAtPredicate(alias string) string {
	vf := alias + "valid_from"
	vt := alias + "valid_to"
	return "(COALESCE(" + vf + ",'')='' OR COALESCE(" + vf + ",'')<=?)" +
		" AND (COALESCE(" + vt + ",'')='' OR COALESCE(" + vt + ",'')>?)"
}

// asOfString normalizes the probe time to the same format writers use.
func asOfString(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// --- functional supersession (spec rev 6) ---

// aliasRoot walks the applied-alias chain X → canonical → ... to its root,
// with a visited-set guard: resolve_entities maintains a forest, but a guard
// costs nothing and a cycle would otherwise loop forever.
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

// idForms returns every ID form an entity's edges may be stored under:
// {X} ∪ {root(X)} ∪ {applied aliases whose chain root is root(X)}. Edges are
// written with pre-resolution LLM IDs and LinkAlias rewrites only derived
// copies, so one logical edge exists under multiple forms; supersession must
// match them all (spec i2-i4).
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
	// SPEC-04 D1: the caller processes forms in order, so the order is
	// observable — make it canonical.
	sort.Strings(out)
	return out
}

func (s *Store) appliedAliasEdges() (map[string]string, error) {
	aliases, err := s.ListAliases(store.AliasApplied)
	if err != nil {
		return nil, fmt.Errorf("ontology.InvalidateFunctional: list aliases: %w", err)
	}
	edges := make(map[string]string, len(aliases))
	for _, a := range aliases {
		edges[a.Alias] = a.CanonicalID
	}
	return edges, nil
}

// InvalidateFunctional implements store.OntologyStore. See the interface doc.
func (s *Store) InvalidateFunctional(sourceID, predicate, keepTargetID, newValidFrom, invalidatedBy string) ([]string, error) {
	if !s.temporalEnabled {
		return nil, nil
	}
	if newValidFrom == "" {
		// Unknown winner date: supersession still happens, with now as the
		// winner's effective start (spec i3 Major) — never skip silently.
		newValidFrom = asOfString(time.Now())
	}
	edges, err := s.appliedAliasEdges()
	if err != nil {
		return nil, err
	}
	sourceForms := idForms(sourceID, edges)
	keepForms := idForms(keepTargetID, edges)

	var invalidated []string
	err = s.db.WriteTx(func(tx *sql.Tx) error {
		// Per-row rule: valid_to = max(newValidFrom, valid_from) — plain
		// string max over RFC3339, NO datetime arithmetic (the earlier +1s
		// clamp left a 1-second overlap window whenever the winner's start
		// was not later than the loser's, breaking same-second corrections —
		// Gate-8 QA). Equality means the winner claims the fact was true at
		// or before the loser started: the loser is then live at NO T
		// (valid_from <= T AND valid_to > T is unsatisfiable), i.e. a clean
		// retroactive win. Empty valid_from → newValidFrom. Invariant:
		// winner and loser are never live at the same T.
		clamp := `CASE WHEN COALESCE(valid_from,'')='' THEN ? ELSE MAX(?, valid_from) END`

		where := `relation = ? AND source_id IN (` + placeholders(len(sourceForms)) + `)` +
			` AND target_id NOT IN (` + placeholders(len(keepForms)) + `)` +
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
			if table == "derived_relations" && !s.derivedExistsFresh() {
				continue // uncached probe: a cached stale-false would skip
				// derived invalidation (read-tuned guard, see derived.go)
			}
			ids, err := func() ([]string, error) {
				rows, err := tx.Query(`SELECT id FROM `+table+` WHERE `+where, mkArgs()...)
				if err != nil {
					return nil, fmt.Errorf("ontology.InvalidateFunctional: scan %s: %w", table, err)
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
			upd := `UPDATE ` + table + ` SET valid_to = ` + clamp +
				`, invalidated_by = ? WHERE id IN (` + placeholders(len(ids)) + `)`
			uargs := []any{newValidFrom, newValidFrom, invalidatedBy}
			for _, id := range ids {
				uargs = append(uargs, id)
			}
			if _, err := tx.Exec(upd, uargs...); err != nil {
				return fmt.Errorf("ontology.InvalidateFunctional: update %s: %w", table, err)
			}
			invalidated = append(invalidated, ids...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// SPEC-07: report every invalidated edge. The winner's start is the
	// effective invalidation boundary; per-row valid_from is not read back
	// (reported as unknown rather than faked).
	if validTo := parseValid(newValidFrom); len(invalidated) > 0 {
		for _, id := range invalidated {
			s.emitEdgeInvalidated(id, invalidatedBy, validTo)
		}
	}
	return invalidated, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// LiveAt reports whether a relation is live at time T, implementing the exact
// liveAtPredicate semantics in Go — one definition shared by the SQL fragment
// and Go-side consumers (P3-5 community detection input building).
func LiveAt(r store.Relation, t time.Time) bool {
	ts := asOfString(t)
	return (r.ValidFrom == "" || r.ValidFrom <= ts) && (r.ValidTo == "" || r.ValidTo > ts)
}
