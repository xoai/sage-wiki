package mirror

import (
	"fmt"
	"strconv"
	"strings"
)

// Bucket key layout, format_version 1 (PUBLIC format — spec.md §Data model):
//
//	<prefix>manifest.json
//	<prefix>mirror-state.json
//	<prefix>objects/docs/<sha2>/<sha256>
//	<prefix>vectors/<sha256>
//	<prefix>db/generation-<N>/snapshot.db.zst
//	<prefix>db/generation-<N>/wal/<SSSSSS>.zst
//	<prefix>db/generation-<N>/meta.json
//
// These are pure functions — every key in the format is built and parsed
// here, never with ad-hoc string joins elsewhere.

// NormalizePrefix returns the prefix with no leading slash and exactly one
// trailing slash ("" stays ""). Whitespace-only is treated as empty.
func NormalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// ManifestKey is the enable-time descriptor, written once.
func ManifestKey(prefix string) string { return prefix + "manifest.json" }

// StateKey is the commit pointer, always written LAST.
func StateKey(prefix string) string { return prefix + "mirror-state.json" }

// DocObjectKey is the content-addressed key for markdown/prompts/sources/
// manifests. sha must be lowercase hex SHA-256 (programmer error otherwise).
func DocObjectKey(prefix, sha string) string {
	mustSHA(sha)
	return prefix + "objects/docs/" + sha[:2] + "/" + sha
}

// VectorObjectKey is the content-addressed key for SWVI index files
// (SPEC-03's top-level /vectors/ prefix, verbatim).
func VectorObjectKey(prefix, sha string) string {
	mustSHA(sha)
	return prefix + "vectors/" + sha
}

// SnapshotKey is the zstd-compressed SQLite snapshot opening generation gen.
func SnapshotKey(prefix string, gen int) string {
	return fmt.Sprintf("%sdb/generation-%d/snapshot.db.zst", prefix, gen)
}

// WALSegmentKey is a sealed WAL segment of generation gen, zero-padded seq.
func WALSegmentKey(prefix string, gen, seq int) string {
	return fmt.Sprintf("%sdb/generation-%d/wal/%06d.zst", prefix, gen, seq)
}

// GenerationMetaKey is the immutable rotation record of a superseded gen.
func GenerationMetaKey(prefix string, gen int) string {
	return fmt.Sprintf("%sdb/generation-%d/meta.json", prefix, gen)
}

// GenerationDirPrefix lists everything under one generation.
func GenerationDirPrefix(prefix string, gen int) string {
	return fmt.Sprintf("%sdb/generation-%d/", prefix, gen)
}

// parseGenerationDirKey extracts the generation from ANY key under
// db/generation-N/ (snapshot, meta, or wal segment) — used to discover
// generation dirs during verification.
func parseGenerationDirKey(key string) (int, bool) {
	i := strings.Index(key, "db/generation-")
	if i < 0 {
		return 0, false
	}
	tail := key[i+len("db/generation-"):]
	j := strings.IndexByte(tail, '/')
	if j <= 0 {
		return 0, false
	}
	gen, err := strconv.Atoi(tail[:j])
	if err != nil {
		return 0, false
	}
	return gen, true
}

// ParseWALSegmentKey extracts (generation, seq) from a segment key.
func ParseWALSegmentKey(key string) (gen, seq int, err error) {
	rest, ok := cutGeneration(key, "/wal/")
	if !ok {
		return 0, 0, fmt.Errorf("layout: not a WAL segment key: %q", key)
	}
	gen, err = strconv.Atoi(rest[0])
	if err != nil {
		return 0, 0, fmt.Errorf("layout: bad generation in %q", key)
	}
	name := strings.TrimSuffix(rest[1], ".zst")
	if len(name) != 6 {
		return 0, 0, fmt.Errorf("layout: unpadded segment seq in %q", key)
	}
	seq, err = strconv.Atoi(name)
	if err != nil {
		return 0, 0, fmt.Errorf("layout: bad segment seq in %q", key)
	}
	return gen, seq, nil
}

// ParseGenerationMetaKey extracts the generation from a meta.json key.
func ParseGenerationMetaKey(key string) (int, error) {
	rest, ok := cutGeneration(key, "/meta.json")
	if !ok || rest[1] != "" {
		return 0, fmt.Errorf("layout: not a generation meta key: %q", key)
	}
	gen, err := strconv.Atoi(rest[0])
	if err != nil {
		return 0, fmt.Errorf("layout: bad generation in %q", key)
	}
	return gen, nil
}

// cutGeneration splits "<prefix>db/generation-<N><sep><rest>" into [N, rest].
func cutGeneration(key, sep string) ([2]string, bool) {
	var out [2]string
	i := strings.Index(key, "db/generation-")
	if i < 0 {
		return out, false
	}
	tail := key[i+len("db/generation-"):]
	j := strings.Index(tail, sep)
	if j < 0 {
		return out, false
	}
	out[0], out[1] = tail[:j], tail[j+len(sep):]
	return out, true
}

// ValidSHA256Hex reports whether s is exactly 64 lowercase hex chars.
func ValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func mustSHA(sha string) {
	if !ValidSHA256Hex(sha) {
		panic(fmt.Sprintf("layout: invalid sha256 %q (programmer error)", sha))
	}
}
