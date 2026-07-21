package postgres

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/memory"
)

// --- timestamp rendering (spec §5 audited per-column table) ---

// fmtSpace renders the datetime('now') family: "2006-01-02 15:04:05" UTC.
func fmtSpace(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// fmtRFC renders the RFC3339 family, UTC.
func fmtRFC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseSpace parses the datetime('now') family as UTC (spec: parse as UTC,
// compare via .Equal/.UTC()).
func parseSpace(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
}

// nullRFC renders an optional RFC3339 field: "" ↔ NULL.
func nullRFC(s string) any {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil // unparsable legacy value → NULL rather than write failure
	}
	return t.UTC()
}

// scanNullRFC reads a NULL-able TIMESTAMPTZ into the "" ↔ NULL contract.
func scanNullRFC(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// --- FTS query building (mirrors memory.buildFTSQuery semantics) ---

// pgStopwords mirrors memory's stopword list (unexported there; standard
// English list — keep in sync with internal/memory/entries.go).
var pgStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"of": true, "in": true, "to": true, "for": true, "with": true,
	"on": true, "at": true, "by": true, "from": true, "as": true,
	"and": true, "or": true, "not": true, "but": true,
	"it": true, "its": true, "this": true, "that": true, "these": true, "those": true,
}

// queryTerms splits a user query into sanitized terms, stopword-filtered,
// falling back to stopword terms when all are stopwords (buildFTSQuery
// parity, including the all-stopwords fallback).
func queryTerms(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	var terms []string
	for _, w := range words {
		w = memory.SanitizeFTS(w)
		if w == "" || pgStopwords[w] {
			continue
		}
		terms = append(terms, w)
	}
	if len(terms) == 0 {
		for _, w := range words {
			w = memory.SanitizeFTS(w)
			if w != "" {
				terms = append(terms, w)
			}
		}
	}
	return terms
}

// tsqueryText builds the to_tsquery input: OR-joined prefix terms ("term:*"
// mirrors FTS5's `"term"*` under prefix='2 3').
func tsqueryText(terms []string) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = t + ":*"
	}
	return strings.Join(parts, " | ")
}

// ftsQuery returns the WHERE fragment and args for a tsvector match over the
// given column expression, with the pinned stopword-evaporation fallback
// (spec §5): if to_tsquery yields empty for a non-empty term set, per-term
// ILIKE on the same expression, OR-combined, LIKE metacharacters escaped.
// nextArg is the next $N index; returns the fragment and the new next index.
func (b *backend) ftsQuery(colExpr string, terms []string, nextArg int) (string, []any, int) {
	if len(terms) == 0 {
		return "TRUE", nil, nextArg
	}
	q := tsqueryText(terms)
	var empty bool
	// Evaluate the tsquery once: snowball evaporates stopword-only queries.
	err := b.pool.QueryRow("SELECT coalesce(to_tsquery('sage_fts', $1)::text = '', true)", q).Scan(&empty)
	if err == nil && empty {
		var likes []string
		var args []any
		for _, t := range terms {
			likes = append(likes, fmt.Sprintf("%s ILIKE $%d", colExpr, nextArg))
			args = append(args, "%"+escapeLike(t)+"%")
			nextArg++
		}
		return "(" + strings.Join(likes, " OR ") + ")", args, nextArg
	}
	return fmt.Sprintf("%s @@ to_tsquery('sage_fts', $%d)", colExpr, nextArg), []any{q}, nextArg + 1
}

// ftsRank returns the ORDER BY rank expression (ts_rank; ILIKE fallback has
// no rank — order by the column for determinism).
func (b *backend) ftsRank(colExpr string, terms []string, nextArg int) (string, []any, int) {
	if len(terms) == 0 {
		return colExpr, nil, nextArg
	}
	q := tsqueryText(terms)
	var empty bool
	err := b.pool.QueryRow("SELECT coalesce(to_tsquery('sage_fts', $1)::text = '', true)", q).Scan(&empty)
	if err == nil && empty {
		return colExpr, nil, nextArg
	}
	return fmt.Sprintf("ts_rank(%s, to_tsquery('sage_fts', $%d)) DESC", colExpr, nextArg), []any{q}, nextArg + 1
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// tagFilter mirrors the sqlite LIKE tag filter (entries.go: tags LIKE
// '%t1,t2%' semantics — any tag match).
func tagFilter(col string, tags []string, nextArg int) (string, []any, int) {
	if len(tags) == 0 {
		return "", nil, nextArg
	}
	var ors []string
	var args []any
	for _, t := range tags {
		ors = append(ors, fmt.Sprintf("%s LIKE $%d", col, nextArg))
		args = append(args, "%"+t+"%")
		nextArg++
	}
	return " AND (" + strings.Join(ors, " OR ") + ")", args, nextArg
}

var _ = sql.ErrNoRows
