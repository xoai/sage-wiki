package memory

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"unicode"

	"github.com/xoai/sage-wiki/internal/store"
)

// Entry represents a searchable wiki entry in FTS5.
// Entry is aliased to store.Entry (P2-1 D2-prime relocation).
type Entry = store.Entry

// Store manages FTS5 entries.
type Store struct {
	db store.DBHandle
}

// NewStore creates a new memory store.
func NewStore(db store.DBHandle) *Store {
	return &Store{db: db}
}

// Add inserts a new entry into the FTS5 index.
// Returns ErrDuplicate if content hash already exists.
func (s *Store) Add(e Entry) error {
	tags := strings.Join(e.Tags, ",")
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO entries (id, content, tags, article_path) VALUES (?, ?, ?, ?)",
			e.ID, e.Content, tags, e.ArticlePath,
		)
		return err
	})
}

// Update replaces an existing entry's content and tags.
func (s *Store) Update(e Entry) error {
	tags := strings.Join(e.Tags, ",")
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"UPDATE entries SET content=?, tags=?, article_path=? WHERE id=?",
			e.Content, tags, e.ArticlePath, e.ID,
		)
		return err
	})
}

// Delete removes an entry by ID.
func (s *Store) Delete(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM entries WHERE id=?", id)
		return err
	})
}

// Get retrieves a single entry by ID.
func (s *Store) Get(id string) (*Entry, error) {
	row := s.db.ReadDB().QueryRow(
		"SELECT id, content, tags, article_path FROM entries WHERE id=?", id,
	)
	var e Entry
	var tags string
	if err := row.Scan(&e.ID, &e.Content, &tags, &e.ArticlePath); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if tags != "" {
		e.Tags = strings.Split(tags, ",")
	}
	return &e, nil
}

// GetMany retrieves entries by ID in batches of sourceDateBatch, so a
// result-set hydration costs one round trip per batch rather than one per
// doc. IDs with no row are absent from the map (the batch twin of Get's
// nil result); duplicate IDs collapse.
func (s *Store) GetMany(ids []string) (map[string]*Entry, error) {
	out := make(map[string]*Entry, len(ids))
	for start := 0; start < len(ids); start += sourceDateBatch {
		end := start + sourceDateBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.db.ReadDB().Query(
			"SELECT id, content, tags, article_path FROM entries WHERE id IN ("+placeholders+")", args...)
		if err != nil {
			return nil, fmt.Errorf("memory.GetMany: %w", err)
		}
		for rows.Next() {
			var e Entry
			var tags string
			if err := rows.Scan(&e.ID, &e.Content, &tags, &e.ArticlePath); err != nil {
				rows.Close()
				return nil, err
			}
			if tags != "" {
				e.Tags = strings.Split(tags, ",")
			}
			entry := e
			out[e.ID] = &entry
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// SearchResult represents a BM25 search hit.
type SearchResult = store.SearchResult

// Search performs BM25 search with optional tag filtering.
func (s *Store) Search(query string, tags []string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Build FTS5 query: OR-joined prefix terms, DF-pruned on large corpora
	ftsQuery := formatFTSTerms(dfPruneTerms(s.db,
		"SELECT COUNT(*) FROM entries",
		"SELECT COUNT(*) FROM entries WHERE entries MATCH ?",
		BuildFTSTerms(query)))
	if ftsQuery == "" {
		return nil, nil
	}

	var args []any
	var tagFilter string

	if len(tags) > 0 {
		// AND pre-filter: all tags must be present
		conditions := make([]string, len(tags))
		for i, tag := range tags {
			conditions[i] = "tags LIKE ?"
			args = append(args, "%"+tag+"%")
		}
		tagFilter = " AND " + strings.Join(conditions, " AND ")
	}

	// Column weights (spec §2.5): id and article_path carry the concept
	// name and act as title proxies (3.0); tags moderate (1.5); content
	// baseline (1.0). bm25() returns negative-better, so ASC order.
	sqlQuery := fmt.Sprintf(`
		SELECT id, content, tags, article_path, bm25(entries, 3.0, 1.0, 1.5, 3.0) AS score
		FROM entries
		WHERE entries MATCH ?%s
		ORDER BY score
		LIMIT ?
	`, tagFilter)

	args = append([]any{ftsQuery}, args...)
	args = append(args, limit)

	rows, err := s.db.ReadDB().Query(sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("memory.Search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	rank := 1
	for rows.Next() {
		var r SearchResult
		var tags string
		var bm25 float64
		if err := rows.Scan(&r.ID, &r.Content, &tags, &r.ArticlePath, &bm25); err != nil {
			return nil, err
		}
		r.BM25Score = -bm25 // FTS5 rank is negative (lower = better)
		r.Rank = rank
		rank++
		if tags != "" {
			r.Tags = strings.Split(tags, ",")
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// Count returns the total number of entries.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.ReadDB().QueryRow("SELECT COUNT(*) FROM entries").Scan(&count)
	return count, err
}

// ContentHash returns the SHA-256 hash of content for deduplication.
func ContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

func buildFTSQuery(query string) string {
	return formatFTSTerms(BuildFTSTerms(query))
}

// buildFTSTerms returns the sanitized, stopword-filtered term list (raw
// words — formatFTSTerms adds quoting and prefix stars).
func BuildFTSTerms(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	var terms []string
	for _, w := range words {
		w = SanitizeFTS(w)
		if w == "" {
			continue
		}
		if !isStopword(w) {
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		// If all words are stopwords, use them anyway
		for _, w := range words {
			w = SanitizeFTS(w)
			if w == "" {
				continue
			}
			terms = append(terms, w)
		}
	}
	return terms
}

// formatFTSTerms renders terms as OR-joined quoted prefix matches.
func formatFTSTerms(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = "\"" + t + "\"*"
	}
	return strings.Join(quoted, " OR ")
}

// DF pruning thresholds (spec §2.5, shared with the postgres twin):
// corpora above DFPruneMinCorpus drop query terms whose prefix-match doc
// frequency exceeds DFPruneMaxRatio; a fully-pruned query keeps its first
// DFPruneKeepFirst terms as the backstop.
const (
	DFPruneMinCorpus = 100
	DFPruneMaxRatio  = 0.2
	DFPruneKeepFirst = 3
)

// dfPruneTerms drops over-frequent terms using the given COUNT probes.
// totalQuery yields the corpus size (documents, never chunks — both legs
// must prune on the same doc-ratio semantics or they diverge); termQuery
// takes the FTS match argument and yields the term's doc frequency.
// Probe failure keeps the term (never silently narrows the query on error).
func dfPruneTerms(db store.DBHandle, totalQuery, termQuery string, terms []string) []string {
	if len(terms) == 0 {
		return terms
	}
	var total int
	if err := db.ReadDB().QueryRow(totalQuery).Scan(&total); err != nil || total <= DFPruneMinCorpus {
		return terms
	}
	maxDF := int(float64(total) * DFPruneMaxRatio)
	kept := terms[:0:0]
	for _, t := range terms {
		var n int
		err := db.ReadDB().QueryRow(termQuery, "\""+t+"\"*").Scan(&n)
		if err != nil || n <= maxDF {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		if len(terms) > DFPruneKeepFirst {
			return terms[:DFPruneKeepFirst]
		}
		return terms
	}
	return kept
}

// SanitizeFTS strips FTS5 special characters to prevent query injection.
// Preserves CJK ideographs, kana, and hangul for multilingual search.
func SanitizeFTS(s string) string {
	var buf strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' ||
			isCJKOrKana(r) {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func isCJKOrKana(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0x3040 && r <= 0x309F) || // Hiragana block
		(r >= 0x30A0 && r <= 0x30FF) // Katakana block (includes prolonged sound mark ー U+30FC)
}

var stopwords = map[string]bool{
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

func isStopword(w string) bool {
	return stopwords[w]
}

// SetSourceDate upserts the entry's origin date (unix seconds) into the
// entry_dates sidecar (ADR-039: "when the knowledge originated", never a
// row timestamp). ts <= 0 is a no-op — "no date" is expressed by absence.
func (s *Store) SetSourceDate(id string, ts int64) error {
	if ts <= 0 {
		return nil
	}
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT INTO entry_dates (id, source_date) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET source_date = excluded.source_date",
			id, ts,
		)
		return err
	})
}

// sourceDateBatch bounds the IN clause well under every driver's bind
// limit (Gate-3 F-067 — an unbounded clause errors the whole call on
// large corpora and no dates would ever backfill).
const sourceDateBatch = 500

// GetSourceDates returns source dates for the given IDs; missing IDs are
// absent from the map (no date — no recency contribution). IDs are
// queried in batches of sourceDateBatch.
func (s *Store) GetSourceDates(ids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(ids))
	for start := 0; start < len(ids); start += sourceDateBatch {
		end := start + sourceDateBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		rows, err := s.db.ReadDB().Query(
			"SELECT id, source_date FROM entry_dates WHERE id IN ("+placeholders+")", args...)
		if err != nil {
			return nil, fmt.Errorf("memory.GetSourceDates: %w", err)
		}
		for rows.Next() {
			var id string
			var ts int64
			if err := rows.Scan(&id, &ts); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = ts
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// ListAll returns every entry, fully populated (P2-1: absorbs reembed's raw
// entries scan). Unbounded by design — reembed needs the full table.
func (s *Store) ListAll() ([]Entry, error) {
	rows, err := s.db.ReadDB().Query("SELECT id, content, tags, article_path FROM entries")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		var tags string
		if err := rows.Scan(&e.ID, &e.Content, &tags, &e.ArticlePath); err != nil {
			return nil, err
		}
		if tags != "" {
			e.Tags = strings.Split(tags, ",")
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountUncompiled counts uncompiled (tier<3) source entries matching a query
// (P2-1: absorbs mcp/server.go's cross-store FTS × compile_items join).
// Tolerance: query/scan errors return 0 (compile_items may not exist on
// legacy vaults — mcp:301 parity). Empty sanitized query returns 0.
func (s *Store) CountUncompiled(query string) (int, error) {
	sanitized := SanitizeFTS(query)
	if sanitized == "" {
		return 0, nil
	}
	var count int
	err := s.db.ReadDB().QueryRow(`
		SELECT COUNT(*) FROM entries
		JOIN compile_items ON compile_items.source_path = SUBSTR(entries.id, 5)
		WHERE entries MATCH ? AND entries.id LIKE 'src:%'
		AND compile_items.tier < 3
	`, sanitized).Scan(&count)
	if err != nil {
		return 0, nil // table may not exist yet (documented tolerance)
	}
	return count, nil
}
