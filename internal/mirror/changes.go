package mirror

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ChangeSource detects unshipped workspace changes (spec.md §Components:
// the SPEC-04 seam — the deterministic change detector SPEC-04 introduces
// plugs in here; the mtime/manifest-diff default lands in Task 14).
type ChangeSource interface {
	// Changes returns workspace changes since the token, and the next token.
	Changes(ctx context.Context, since ChangeToken) ([]Change, ChangeToken, error)
}

// ChangeToken carries the committed object maps the diff runs against
// (from the remote mirror-state — the only shared truth).
type ChangeToken struct {
	Committed        map[string]ObjectRef
	CommittedVectors map[string]ObjectRef
}

// ChangeKind classifies a detected change.
type ChangeKind int

const (
	// ChangeUpsert is a new or modified file.
	ChangeUpsert ChangeKind = iota
	// ChangeDelete is a removed file (tombstone).
	ChangeDelete
)

// Change is one detected workspace change.
type Change struct {
	Path   string // workspace-relative slash path (vectors: basename)
	Kind   ChangeKind
	SHA256 string // content hash (upserts only)
	Vector bool   // true for .sage/vectors*.idx entries (top-level vectors/ prefix)
}

// shipSetDirs are walked recursively (missing is fine — pre-init workspace).
var shipSetDirs = []string{"wiki", "raw", "prompts"}

// shipSetFiles are individual shipped files.
var shipSetFiles = []string{".manifest.json", ".sage/manifest.json", ".sage/pack-state.yaml"}

// excludedBases are never shipped — but ONLY under .sage/ (their home);
// config.yaml is excluded at the ROOT only (F-083: user content under
// wiki/raw/prompts named config.yaml or jobs.jsonl is CONTENT and ships —
// the exclusions protect process files, not names). spec.md §Key
// decisions 7. .sage/ as a whole is covered by the shipSetFiles allowlist
// (only explicitly named files ship), so no directory exclusions are
// needed there (F-090's dead list removed).
var excludedBases = map[string]bool{
	"engine.lock":           true,
	"jobs.jsonl":            true,
	"batch-state.json":      true,
	"compile-state.json":    true,
	"usage.jsonl":           true,
	"mirror-local.json":     true,
	"mirror-ship.lock":      true,
	"mirror-diffcache.json": true,
	"hydrate-state.json":    true,
	"wiki.db":               true,
	"wiki.db-wal":           true,
	"wiki.db-shm":           true,
}

// excludedPath reports whether rel (slash path) is excluded: config.yaml at
// the ROOT only, excludedBases under .sage/ only.
func excludedPath(rel string) bool {
	if rel == "config.yaml" {
		return true
	}
	if !strings.HasPrefix(rel, ".sage/") {
		return false
	}
	return excludedBases[filepath.Base(rel)]
}

// diffChangeSource is the default mtime/manifest-diff detector. It keeps a
// persistent stat cache (.sage/mirror-diffcache.json — machine-local,
// never shipped) so an idle pass hashes NOTHING (F-082): mtime+size match
// reuses the cached sha, anything else rehashes just that file.
type diffChangeSource struct {
	dir       string
	cache     map[string]diffCacheEntry
	cachePath string
	readOnly  bool
}

type diffCacheEntry struct {
	SHA256   string `json:"s"`
	ModUnix  int64  `json:"m"`
	Size     int64  `json:"z"`
	WriteSec int64  `json:"w"` // hashed-at second (git racy-clean: hit only when ModUnix < WriteSec)
}

// NewDiffChangeSource walks the ship set and diffs against the committed
// object maps in the token.
func NewDiffChangeSource(dir string) ChangeSource {
	d := &diffChangeSource{
		dir:       dir,
		cachePath: filepath.Join(dir, ".sage", "mirror-diffcache.json"),
		cache:     map[string]diffCacheEntry{},
	}
	d.loadCache()
	return d
}

// NewDiffChangeSourceReadOnly is the status-path variant (F-097): reads the
// cache but never writes it — read-only commands don't mutate the workspace.
func NewDiffChangeSourceReadOnly(dir string) ChangeSource {
	d := &diffChangeSource{
		dir:       dir,
		cachePath: filepath.Join(dir, ".sage", "mirror-diffcache.json"),
		cache:     map[string]diffCacheEntry{},
		readOnly:  true,
	}
	d.loadCache()
	return d
}

func (d *diffChangeSource) loadCache() {
	b, err := os.ReadFile(d.cachePath)
	if err != nil {
		return // missing/corrupt cache → cold start (hashes repopulate it)
	}
	var m map[string]diffCacheEntry
	if json.Unmarshal(b, &m) == nil && m != nil {
		d.cache = m
	}
}

func (d *diffChangeSource) saveCache() {
	b, err := json.Marshal(d.cache)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(d.cachePath), 0o755); err != nil {
		return
	}
	tmp := d.cachePath + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, d.cachePath)
	}
}

// Changes computes the diff: upserts for new/modified/resurrected files,
// tombstones for committed paths no longer on disk. Deterministic order
// (sorted paths) — no map-order iteration reaches any serialized output.
func (d *diffChangeSource) Changes(ctx context.Context, since ChangeToken) ([]Change, ChangeToken, error) {
	present := map[string]string{} // rel path → sha256 (docs set)
	scanStartSec := timeNowUnix()
	onDisk := func(rel string) (string, bool) {
		full := filepath.Join(d.dir, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			return "", false
		}
		// Racy-clean guard (F-096, git's rule): an entry cached in the SAME
		// second as this scan is re-hashed — a same-size rewrite inside one
		// mtime second (or a preserved mtime) must never hide an edit.
		// Racy-clean guard (F-096/PB-2, git's rule done right): an entry is
		// trusted only when its recorded mtime-second is OLDER than the
		// second it was hashed in — an mtime equal to the hash-second may
		// have changed after the hash within that second. Legacy entries
		// (WriteSec 0) miss once and self-heal.
		if ent, ok := d.cache[rel]; ok && ent.ModUnix == info.ModTime().Unix() && ent.Size == info.Size() && ent.ModUnix < ent.WriteSec {
			return ent.SHA256, true // cache hit — NO file read (F-082)
		}
		sha, _, err := hashFile(full)
		if err != nil {
			return "", false
		}
		d.cache[rel] = diffCacheEntry{SHA256: sha, ModUnix: info.ModTime().Unix(), Size: info.Size(), WriteSec: scanStartSec}
		return sha, true
	}

	addFile := func(rel string) error {
		sha, ok := onDisk(rel)
		if ok {
			present[rel] = sha
		}
		return nil
	}

	for _, dir := range shipSetDirs {
		root := filepath.Join(d.dir, dir)
		if _, err := os.Stat(root); err != nil {
			continue // missing ship-set dir is fine
		}
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(d.dir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel) + "/"
			if e.IsDir() {
				return nil
			}
			rel = strings.TrimSuffix(rel, "/")
			if excludedPath(rel) || strings.HasSuffix(e.Name(), ".tmp") {
				return nil
			}
			return addFile(rel)
		})
		if err != nil {
			return nil, since, err
		}
	}
	for _, rel := range shipSetFiles {
		if err := addFile(rel); err != nil {
			return nil, since, err
		}
	}

	// Vectors: glob .sage/vectors*.idx → diffed against CommittedVectors by
	// basename (the committed map keys are file names, not paths).
	vectorsPresent := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(d.dir, ".sage", "vectors*.idx"))
	for _, p := range matches {
		sha, ok := onDisk(filepath.ToSlash(mustRel(d.dir, p)))
		if ok {
			vectorsPresent[filepath.Base(p)] = sha
		}
	}

	var changes []Change
	for rel, sha := range present {
		ref, ok := since.Committed[rel]
		if !ok || ref.Deleted || committedContentSHA(ref) != sha {
			changes = append(changes, Change{Path: rel, Kind: ChangeUpsert, SHA256: sha})
		}
	}
	for rel, ref := range since.Committed {
		if ref.Deleted {
			continue
		}
		if _, ok := present[rel]; !ok {
			changes = append(changes, Change{Path: rel, Kind: ChangeDelete})
		}
	}
	for name, sha := range vectorsPresent {
		ref, ok := since.CommittedVectors[name]
		if !ok || ref.Deleted || committedContentSHA(ref) != sha {
			changes = append(changes, Change{Path: name, Kind: ChangeUpsert, SHA256: sha, Vector: true})
		}
	}
	for name, ref := range since.CommittedVectors {
		if ref.Deleted {
			continue
		}
		if _, ok := vectorsPresent[name]; !ok {
			changes = append(changes, Change{Path: name, Kind: ChangeDelete, Vector: true})
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	// Prune cache entries for vanished files, then persist (atomic).
	for rel := range d.cache {
		if _, ok := present[rel]; !ok {
			if _, isVec := vectorsPresent[filepath.Base(rel)]; !isVec {
				delete(d.cache, rel)
			}
		}
	}
	if !d.readOnly {
		d.saveCache()
	}
	return changes, since, nil
}

// committedContentSHA is the hash the diff compares against: the plaintext
// content hash when encrypted (ContentSHA256), else the shipped-bytes hash.
func committedContentSHA(ref ObjectRef) string {
	if ref.ContentSHA256 != "" {
		return ref.ContentSHA256
	}
	return ref.SHA256
}

var timeNowUnix = func() int64 { return time.Now().Unix() }

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
