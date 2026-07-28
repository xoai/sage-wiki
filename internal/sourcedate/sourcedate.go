package sourcedate

import (
	"bytes"
	"os"
	"regexp"
	"time"
)

// frontmatterDateRe matches a `date:` key in a leading YAML frontmatter
// block. Accepted values: YYYY-MM-DD or RFC3339 (quoted or bare).
var frontmatterDateRe = regexp.MustCompile(`(?m)^date:\s*["']?([0-9]{4}-[0-9]{2}-[0-9]{2}(?:[T ][0-9:+.Z-]+)?)["']?\s*$`)

// maxFrontmatterProbe bounds how much of a file the date probe reads.
const maxFrontmatterProbe = 4096

// ResolveSourceDate implements the ADR-039 origin-date chain for a source
// file: frontmatter `date:` > file mtime > manifest first-seen (RFC3339,
// may be empty). Returns unix seconds, or 0 when nothing in the chain
// yields a date ("no date" — absence, never a fabricated timestamp).
func Resolve(absPath, manifestAddedAt string) int64 {
	if ts := frontmatterDate(absPath); ts > 0 {
		return ts
	}
	if fi, err := os.Stat(absPath); err == nil {
		return fi.ModTime().Unix()
	}
	if manifestAddedAt != "" {
		if t, err := time.Parse(time.RFC3339, manifestAddedAt); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// frontmatterDate extracts a date from the file's leading frontmatter
// block. Returns 0 when absent or unparseable.
func frontmatterDate(absPath string) int64 {
	f, err := os.Open(absPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, maxFrontmatterProbe)
	n, _ := f.Read(buf)
	head := buf[:n]
	if len(head) < 4 || string(head[:4]) != "---\n" {
		return 0
	}
	// Bound the search to the frontmatter block — a body `date:` line
	// after the closing --- must never match.
	block := head[4:]
	if end := bytes.Index(block, []byte("\n---")); end >= 0 {
		block = block[:end+1]
	}
	m := frontmatterDateRe.FindSubmatch(block)
	if m == nil {
		return 0
	}
	raw := string(m[1])
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// MaxSourceDate returns the latest date among the given entry IDs' stored
// dates — compiled articles are "as fresh as their newest evidence"
// (M3 decision; 0 when no contributing source has a date).
func Max(dates map[string]int64, ids []string) int64 {
	var max int64
	for _, id := range ids {
		if ts := dates[id]; ts > max {
			max = ts
		}
	}
	return max
}
