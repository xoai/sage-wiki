package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
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
// parity, including the all-stopwords fallback). Hyphenated words split
// into their parts (FTS5/unicode61 tokenizes on hyphens — pg snowball
// treats them as operators, so parts are the parity-preserving form).
func queryTerms(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	var terms []string
	add := func(w string) {
		w = memory.SanitizeFTS(w)
		if w == "" || pgStopwords[w] {
			return
		}
		if strings.Contains(w, "-") {
			for _, part := range strings.Split(w, "-") {
				if part != "" && !pgStopwords[part] {
					terms = append(terms, part)
				}
			}
			return
		}
		terms = append(terms, w)
	}
	for _, w := range words {
		add(w)
	}
	if len(terms) == 0 {
		for _, w := range words {
			w = memory.SanitizeFTS(w)
			if w == "" {
				continue
			}
			// All-stopwords fallback uses the terms anyway — with the same
			// hyphen splitting as the main path.
			if strings.Contains(w, "-") {
				for _, part := range strings.Split(w, "-") {
					if part != "" {
						terms = append(terms, part)
					}
				}
				continue
			}
			terms = append(terms, w)
		}
	}
	return terms
}

// ftsPlan is a single-probe FTS plan for one search call: the WHERE
// fragment, the ORDER BY rank expression, and their args. The tsquery is
// evaluated ONCE (no double round trip); on empty evaporation (stopword
// storm) or probe error, the pinned fallback is per-term ILIKE on textExpr
// with the text expression as deterministic rank.
type ftsPlan struct {
	where string
	rank  string
	args  []any
	next  int
}

// dfPruneTerms is the pg twin of memory.dfPruneTerms (spec §2.5): on
// corpora above memory.DFPruneMinCorpus, drop terms whose doc frequency
// exceeds memory.DFPruneMaxRatio; keep the first memory.DFPruneKeepFirst
// when everything would be dropped. Probe failure keeps the term.
// totalQuery counts DOCUMENTS; termQuery takes the ts prefix query and
// counts the term's document frequency (chunks_meta counts DISTINCT
// doc_id so both legs share doc-ratio semantics — F-047).
func (b *backend) dfPruneTerms(totalQuery, termQuery string, terms []string) []string {
	if len(terms) == 0 {
		return terms
	}
	var total int
	if err := b.pool.QueryRow(totalQuery).Scan(&total); err != nil || total <= memory.DFPruneMinCorpus {
		return terms
	}
	maxDF := int(float64(total) * memory.DFPruneMaxRatio)
	kept := terms[:0:0]
	for _, t := range terms {
		var n int
		err := b.pool.QueryRow(termQuery, t+":*").Scan(&n)
		if err != nil || n <= maxDF {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		if len(terms) > memory.DFPruneKeepFirst {
			return terms[:memory.DFPruneKeepFirst]
		}
		return terms
	}
	return kept
}

func (b *backend) planFTS(tsvCol, textExpr string, terms []string, nextArg int) ftsPlan {
	if len(terms) == 0 {
		return ftsPlan{where: "TRUE", rank: textExpr, next: nextArg}
	}
	q := tsqueryText(terms)
	var empty bool
	// One probe round trip per search call - the price of detecting
	// stopword evaporation server-side; accepted latency tradeoff.
	err := b.pool.QueryRow("SELECT coalesce(to_tsquery('sage_fts', $1)::text = '', true)", q).Scan(&empty)
	if err != nil {
		// Persistent probe failure (e.g. sage_fts dropped) degrades to ILIKE —
		// observable, not silent.
		log.Warn("fts tsquery probe failed — falling back to ILIKE", "error", err)
	}
	if err != nil || empty {
		p := ftsPlan{rank: textExpr, next: nextArg}
		var likes []string
		for _, t := range terms {
			likes = append(likes, fmt.Sprintf("%s ILIKE $%d", textExpr, p.next))
			p.args = append(p.args, "%"+escapeLike(t)+"%")
			p.next++
		}
		p.where = "(" + strings.Join(likes, " OR ") + ")"
		return p
	}
	return ftsPlan{
		where: fmt.Sprintf("%s @@ to_tsquery('sage_fts', $%d)", tsvCol, nextArg),
		rank:  fmt.Sprintf("ts_rank(%s, to_tsquery('sage_fts', $%d)) DESC", tsvCol, nextArg),
		args:  []any{q},
		next:  nextArg + 1,
	}
}

// tsqueryText builds the to_tsquery input: OR-joined prefix terms ("term:*"
// mirrors FTS5's `"term"*` under prefix='2 3').
func tsqueryText(terms []string) string {
	parts := make([]string, 0, len(terms))
	for _, t := range terms {
		parts = append(parts, t+":*")
	}
	return strings.Join(parts, " | ")
}

// tagFilter mirrors the sqlite LIKE tag filter: AND pre-filter — ALL tags
// must be present (memory/entries.go:96-105 parity; LIKE wildcards in tags
// are unescaped exactly as on sqlite).
func tagFilter(col string, tags []string, nextArg int) (string, []any, int) {
	if len(tags) == 0 {
		return "", nil, nextArg
	}
	var conds []string
	var args []any
	for _, t := range tags {
		conds = append(conds, fmt.Sprintf("%s LIKE $%d", col, nextArg))
		args = append(args, "%"+t+"%")
		nextArg++
	}
	return " AND " + strings.Join(conds, " AND "), args, nextArg
}

// normLimit applies a sqlite default: entries/vector search 10, chunk
// search 20 (memory/chunks.go:71, vectors/store.go:223,259,281).
func normLimit(limit, def int) int {
	if limit <= 0 {
		return def
	}
	return limit
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
