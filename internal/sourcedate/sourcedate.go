package sourcedate

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// frontmatterDateRe matches a `date:` key in a leading YAML frontmatter
// block. Accepted values: YYYY-M-D (padded or not) or a timestamp form
// (quoted or bare).
var frontmatterDateRe = regexp.MustCompile(`(?m)^date:\s*["']?([0-9]{4}-[0-9]{1,2}-[0-9]{1,2}(?:[T ][0-9:+.Z-]+)?)["']?\s*$`)

// dateLayouts are tried in order against a matched frontmatter value.
var dateLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05", // TZ-less T-form (common in exported notes)
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006-1-2", // non-zero-padded
}

// maxFrontmatterProbe bounds how much of a file the date probe reads.
const maxFrontmatterProbe = 4096

// Resolve implements the ADR-039 origin-date chain for a source file:
// frontmatter `date:` > file mtime > manifest first-seen (RFC3339, may be
// empty). Returns unix seconds, or 0 when nothing in the chain yields a
// date ("no date" — absence, never a fabricated timestamp).
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
// block. Returns 0 when absent, unclosed, or unparseable. CRLF line
// endings and a UTF-8 BOM are tolerated; the search never leaves the
// frontmatter block — an unclosed block within the probe window is
// treated as no frontmatter, so a body `date:` line never fabricates an
// origin date.
func frontmatterDate(absPath string) int64 {
	f, err := os.Open(absPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, maxFrontmatterProbe)
	n, _ := f.Read(buf)
	head := buf[:n]
	head = bytes.TrimPrefix(head, []byte{0xEF, 0xBB, 0xBF})     // BOM
	head = bytes.ReplaceAll(head, []byte("\r\n"), []byte("\n")) // CRLF
	if len(head) < 4 || string(head[:4]) != "---\n" {
		return 0
	}
	block := head[4:]
	end := bytes.Index(block, []byte("\n---"))
	if end < 0 {
		return 0 // unclosed within the window — not frontmatter
	}
	block = block[:end+1]
	m := frontmatterDateRe.FindSubmatch(block)
	if m == nil {
		return 0
	}
	raw := string(m[1])
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Unix()
		}
	}
	log.Warn("frontmatter date matched but no layout parses it — falling back", "file", absPath, "value", raw)
	return 0
}

// Max returns the latest date among the given entry IDs' stored dates —
// compiled articles are "as fresh as their newest evidence" (M3 decision;
// 0 when no contributing source has a date).
func Max(dates map[string]int64, ids []string) int64 {
	var max int64
	for _, id := range ids {
		if ts := dates[id]; ts > max {
			max = ts
		}
	}
	return max
}

// RecordForSource resolves a source's origin date once and records it for
// BOTH of the source's searchable identities: the summary entry (bare
// path ID — the default Tier-3 doc class) and the src:-prefixed raw
// entry (which concept max-of-sources reads). Errors are logged, never
// fatal — a missing date must not fail a compile.
func RecordForSource(mem store.EntryStore, projectDir, srcPath, manifestAddedAt string) {
	ts := Resolve(filepath.Join(projectDir, srcPath), manifestAddedAt)
	if ts <= 0 {
		return
	}
	for _, id := range []string{srcPath, "src:" + srcPath} {
		if err := mem.SetSourceDate(id, ts); err != nil {
			log.Warn("source date not recorded", "id", id, "error", err)
		}
	}
}
