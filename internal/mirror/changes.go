package mirror

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	Path    string // workspace-relative slash path (vectors: basename)
	Kind    ChangeKind
	SHA256  string // content hash (upserts only)
	ModUnix int64
	Vector  bool // true for .sage/vectors*.idx entries (top-level vectors/ prefix)
}

// shipSetDirs are walked recursively (missing is fine — pre-init workspace).
var shipSetDirs = []string{"wiki", "raw", "prompts"}

// shipSetFiles are individual shipped files.
var shipSetFiles = []string{".manifest.json", ".sage/manifest.json", ".sage/pack-state.yaml"}

// excludedBases are never shipped, anywhere (spec.md §Key decisions 7 —
// config.yaml first: it can carry inline secrets, F-001).
var excludedBases = map[string]bool{
	"config.yaml":        true,
	"engine.lock":        true,
	"jobs.jsonl":         true,
	"batch-state.json":   true,
	"compile-state.json": true,
	"usage.jsonl":        true,
	"mirror-local.json":  true,
	"mirror-ship.lock":   true,
	"hydrate-state.json": true,
	"wiki.db":            true,
	"wiki.db-wal":        true,
	"wiki.db-shm":        true,
}

// excludedDirs are never shipped (prefix match on the relative path).
var excludedDirs = []string{".sage/lintlog/", ".sage/pack-snapshots/"}

// diffChangeSource is the default mtime/manifest-diff detector.
type diffChangeSource struct{ dir string }

// NewDiffChangeSource walks the ship set and diffs against the committed
// object maps in the token.
func NewDiffChangeSource(dir string) ChangeSource {
	return &diffChangeSource{dir: dir}
}

// Changes computes the diff: upserts for new/modified/resurrected files,
// tombstones for committed paths no longer on disk. Deterministic order
// (sorted paths) — no map-order iteration reaches any serialized output.
func (d *diffChangeSource) Changes(ctx context.Context, since ChangeToken) ([]Change, ChangeToken, error) {
	present := map[string]string{} // rel path → sha256 (docs set)
	onDisk := func(rel string) (string, bool) {
		sha, _, err := hashFile(filepath.Join(d.dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", false
		}
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
				for _, ex := range excludedDirs {
					if rel == strings.TrimSuffix(ex, "/")+"/" || strings.HasPrefix(rel, ex) {
						return filepath.SkipDir
					}
				}
				return nil
			}
			rel = strings.TrimSuffix(rel, "/")
			if excludedBases[e.Name()] || strings.HasSuffix(e.Name(), ".tmp") {
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
		if !ok || ref.Deleted || ref.SHA256 != sha {
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
		if !ok || ref.Deleted || ref.SHA256 != sha {
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
	return changes, since, nil
}

func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}
