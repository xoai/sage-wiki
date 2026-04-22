package memory

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/xoai/sage-wiki/internal/storage"
)

// Entry represents a searchable wiki entry in FTS5.
type Entry struct {
	ID          string
	Content     string
	Tags        []string
	ArticlePath string
	CreatedAt   time.Time
}

// Store manages FTS5 entries.
type Store struct {
	db *storage.DB
}

// NewStore creates a new memory store.
func NewStore(db *storage.DB) *Store {
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

// SearchResult represents a BM25 search hit.
type SearchResult struct {
	ID          string
	Content     string
	Tags        []string
	ArticlePath string
	BM25Score   float64
	Rank        int
}

// Search performs BM25 search with optional tag filtering.
func (s *Store) Search(query string, tags []string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	// Build FTS5 query: OR-joined prefix terms
	ftsQuery := buildFTSQuery(query)
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

	sqlQuery := fmt.Sprintf(`
		SELECT id, content, tags, article_path, rank
		FROM entries
		WHERE entries MATCH ?%s
		ORDER BY rank
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

// buildFTSQuery converts a user query into FTS5 OR-joined prefix terms.
// Stopwords are filtered out. FTS5 special characters are stripped for safety.
func buildFTSQuery(query string) string {
	words := strings.Fields(strings.ToLower(query))
	var terms []string
	for _, w := range words {
		w = SanitizeFTS(w)
		if w == "" {
			continue
		}
		if !isStopword(w) {
			terms = append(terms, "\""+w+"\"*")
		}
	}
	if len(terms) == 0 {
		// If all words are stopwords, use them anyway
		for _, w := range words {
			w = SanitizeFTS(w)
			if w == "" {
				continue
			}
			terms = append(terms, "\""+w+"\"*")
		}
	}
	return strings.Join(terms, " OR ")
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
