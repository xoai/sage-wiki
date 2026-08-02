package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// Hydrate restores a mirrored workspace into an EMPTY dir (spec.md §Hydrate):
// ordered restore — manifest → db (snapshot + WAL replay) → markdown →
// vectors — with checksum verification and point-in-time selection.
// The pre-workspace CLI path passes a bare client+RemoteRef; the facade
// passes its own. cfg carries endpoint/bucket/prefix/region for the client
// built inside (creds resolved by the caller into s3.Credentials via cfg).
func Hydrate(ctx context.Context, cfg Config, dst string, opts HydrateOpts) (*Report, error) {
	cfg.normalize()
	creds, err := ResolveCredentials(cfg.AccessKeyEnv, cfg.SecretKeyEnv, cfg.SessionTokenEnv, cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	client, err := s3.NewClient(cfg.Endpoint, cfg.Region, creds, s3.WithPathStyle(cfg.pathStyle()))
	if err != nil {
		return nil, err
	}
	return hydrateWithClient(ctx, client, NormalizePrefix(cfg.Prefix), cfg.Bucket, dst, opts)
}

// Hydrate implements the pkg seam (newest generation, full restore).
func (o *mirrorOps) Hydrate(ctx context.Context, dst string) error {
	_, err := Hydrate(ctx, o.m.cfg, dst, HydrateOpts{})
	return err
}

// hydrateWithClient is the restore engine.
func hydrateWithClient(ctx context.Context, client *s3.Client, prefix, bucket, dst string, opts HydrateOpts) (*Report, error) {
	rep := &Report{}

	// Empty-dir rule (no merge semantics). A --partial resume into a dir
	// with a progress marker is the one exception (spec: a follow-up
	// hydrate --partial resumes incomplete phases).
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		resume := opts.Partial
		if resume {
			if _, serr := os.Stat(filepath.Join(dst, ".sage", "hydrate-state.json")); serr != nil {
				resume = false
			}
		}
		if !resume {
			return nil, fmt.Errorf("hydrate: %s is not empty (restore requires an empty dir)", dst)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("hydrate: read dst: %w", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, fmt.Errorf("hydrate: create dst: %w", err)
	}

	// Single-hydrate lock (advisory; same file pattern as the ship-mutex).
	mutex, err := AcquireShipMutex(dst, 0)
	if err != nil {
		return nil, fmt.Errorf("hydrate: %w", err)
	}
	defer mutex.Release()

	// Manifest gate: encrypted mirrors require a key (Task 21 wires the
	// decryptor; the gate itself is loud NOW).
	manifest, manErr := loadMirrorManifest(ctx, client, bucket, prefix)
	if manErr != nil {
		return nil, manErr
	}
	var encKey []byte
	if manifest.Encrypted {
		if opts.KeyFile == "" {
			return nil, fmt.Errorf("hydrate: mirror is encrypted — pass --key-file")
		}
		k, err := LoadEncryptionKey(opts.KeyFile)
		if err != nil {
			return nil, err
		}
		encKey = k
	}

	// getPlain downloads, verifies the SHIPPED-bytes sha, and decrypts when
	// the mirror is encrypted (AEAD failure names the object — never a
	// partial-silent restore).
	getPlain := func(key, wantSHA string) ([]byte, error) {
		b, err := downloadVerified(ctx, client, bucket, key, wantSHA)
		if err != nil {
			return nil, err
		}
		if encKey != nil {
			plain, err := decryptBytes(encKey, b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			return plain, nil
		}
		return b, nil
	}

	// Load the commit pointer.
	sb, err := client.GetObject(ctx, bucket, StateKey(prefix))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return nil, fmt.Errorf("hydrate: no mirror-state.json at s3://%s/%s (mirror not enabled?)", bucket, prefix)
		}
		return nil, fmt.Errorf("hydrate: read state: %w", err)
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		return nil, err
	}
	if err := st.Validate(); err != nil {
		return nil, fmt.Errorf("hydrate: remote state invalid: %w", err)
	}

	// Select the restore point.
	sel, err := selectRestorePoint(ctx, client, prefix, bucket, st, opts)
	if err != nil {
		return nil, err
	}
	phases := newHydrateProgress(dst, opts)
	if err := phases.load(); err != nil {
		return nil, err
	}

	rep.Generation = sel.generation
	atStr := ""
	if !opts.At.IsZero() {
		atStr = opts.At.UTC().Format(time.RFC3339)
	}
	phases.generation = sel.generation
	phases.at = atStr
	if err := phases.checkSelection(sel.generation, atStr); err != nil {
		return nil, err
	}
	if sel.fallbackNote != "" {
		rep.Advisories = append(rep.Advisories, sel.fallbackNote)
	}
	var skewParts []string
	if sel.overshoot > 0 {
		skewParts = append(skewParts, fmt.Sprintf("%d segment(s) sealed after the requested time excluded", sel.overshoot))
	}
	if sel.fromMeta && sel.objectSkew {
		skewParts = append(skewParts, fmt.Sprintf("objects at generation %d's seal", sel.generation))
	} else if !sel.fromMeta && !opts.At.IsZero() {
		// Live gen selected by --at: the map is always "newest" — name it
		// even with zero excluded segments (docs may have changed after T).
		skewParts = append(skewParts, "objects are at newest (live map)")
	}
	if len(skewParts) > 0 {
		rep.Overshoot = strings.Join(skewParts, "; ")
	}

	if err := os.MkdirAll(filepath.Join(dst, ".sage"), 0o755); err != nil {
		return nil, err
	}

	// --- Phase: db (snapshot + WAL replay) ---
	dbPath := filepath.Join(dst, ".sage", "wiki.db")
	if !phases.alreadyDone("db") {
		dbBytes, err := getPlain(sel.snapshot, sel.snapshotSHA)
		if err != nil {
			return nil, err
		}
		dbRaw, err := zstdDecode(dbBytes)
		if err != nil {
			return nil, fmt.Errorf("hydrate: decompress snapshot: %w", err)
		}
		if err := os.WriteFile(dbPath, dbRaw, 0o644); err != nil {
			return nil, fmt.Errorf("hydrate: write db: %w", err)
		}
		// WAL replay: concatenate segment bytes (seq order; seq 1 carries the
		// 32-byte header; later segments are frame ranges).
		if len(sel.wal) > 0 {
			var walBytes []byte
			for _, seg := range sel.wal {
				b, err := getPlain(seg.Key, seg.SHA256)
				if err != nil {
					return nil, err
				}
				raw, err := zstdDecode(b)
				if err != nil {
					return nil, fmt.Errorf("hydrate: decompress %s: %w", seg.Key, err)
				}
				walBytes = append(walBytes, raw...)
			}
			if err := os.WriteFile(dbPath+"-wal", walBytes, 0o644); err != nil {
				return nil, fmt.Errorf("hydrate: write wal: %w", err)
			}
		}
	}

	if err := phases.complete("db"); err != nil {
		return nil, err
	}

	// --- Phase: markdown/docs (skip tombstones; per-class rule: abort on
	// mismatch naming the object) ---
	if !phases.alreadyDone("markdown") {
		// Path confinement (F-084): mirror-state is data — a poisoned or buggy
		// state must never write outside the restore dir.
		for p := range sel.objects {
			if err := confineRelPath(p); err != nil {
				return nil, fmt.Errorf("hydrate: unsafe object path %q: %w", p, err)
			}
		}

		paths := make([]string, 0, len(sel.objects))
		for p := range sel.objects {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			ref := sel.objects[p]
			if ref.Deleted {
				continue
			}
			b, err := getPlain(ref.Key, ref.SHA256)
			if err != nil {
				return nil, fmt.Errorf("hydrate: %w", err)
			}
			dstPath := filepath.Join(dst, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dstPath, b, 0o644); err != nil {
				return nil, fmt.Errorf("hydrate: write %s: %w", p, err)
			}
			rep.Checked++
		}
	}

	if err := phases.complete("markdown"); err != nil {
		return nil, err
	}
	// Ordered availability: lexical/graph serve from here (spec AC-4).
	if opts.Partial {
		rep.Advisories = append(rep.Advisories, "lexical/graph available — vectors phase follows")
	}

	// --- Phase: vectors (warn + continue — rebuildable) ---
	if !phases.alreadyDone("vectors") {
		vecNames := make([]string, 0, len(sel.vectors))
		for n := range sel.vectors {
			vecNames = append(vecNames, n)
		}
		sort.Strings(vecNames)
		for _, name := range vecNames {
			// F-095: vector names are basenames BY CONVENTION (changes.go);
			// anything else is a poisoned state — reject, never write.
			if name != filepath.Base(name) {
				return nil, fmt.Errorf("hydrate: unsafe vector name %q (not a basename)", name)
			}
			ref := sel.vectors[name]
			if ref.Deleted {
				continue
			}
			b, err := getPlain(ref.Key, ref.SHA256)
			if err != nil {
				rep.Advisories = append(rep.Advisories, fmt.Sprintf("vector %s not restored (rebuild with `index rebuild-vectors`): %v", name, err))
				continue
			}
			if err := os.WriteFile(filepath.Join(dst, ".sage", name), b, 0o644); err != nil {
				return nil, fmt.Errorf("hydrate: write vector %s: %w", name, err)
			}
			rep.Checked++
		}
	}

	if err := phases.complete("vectors"); err != nil {
		return nil, err
	}
	rep.Valid = true
	rep.RestoredTo = dst
	return rep, nil
}

// loadMirrorManifest reads <prefix>manifest.json (absent = pre-format
// mirror, treated as unencrypted).
func loadMirrorManifest(ctx context.Context, client *s3.Client, bucket, prefix string) (*MirrorManifest, error) {
	mb, err := client.GetObject(ctx, bucket, ManifestKey(prefix))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return &MirrorManifest{FormatVersion: FormatVersion}, nil
		}
		return nil, fmt.Errorf("hydrate: read manifest.json: %w", err)
	}
	var man MirrorManifest
	if err := json.Unmarshal(mb, &man); err != nil {
		return nil, fmt.Errorf("hydrate: parse manifest.json: %w", err)
	}
	return &man, nil
}

// hydrateProgress tracks --partial phase completion in
// .sage/hydrate-state.json; resume skips completed phases.
type hydrateProgress struct {
	path       string
	enabled    bool
	generation int
	at         string
	Done       map[string]bool
}

// progressFile is the on-disk shape (selection recorded — a resume with a
// different restore point must not mix phases, F-107).
type progressFile struct {
	Generation int      `json:"generation"`
	At         string   `json:"at,omitempty"`
	Phases     []string `json:"phases"`
}

func newHydrateProgress(dst string, opts HydrateOpts) *hydrateProgress {
	return &hydrateProgress{
		path:    filepath.Join(dst, ".sage", "hydrate-state.json"),
		enabled: opts.Partial,
		Done:    map[string]bool{},
	}
}

// checkSelection refuses a resume whose selection differs from the recorded
// one (mixing restore points would silently mix phases).
func (p *hydrateProgress) checkSelection(gen int, at string) error {
	if !p.enabled || len(p.Done) == 0 {
		return nil
	}
	b, err := os.ReadFile(p.path)
	if err != nil {
		return nil // no recorded selection (legacy/parallel) — proceed
	}
	var pf progressFile
	if json.Unmarshal(b, &pf); err != nil {
		return nil
	}
	if pf.Generation != gen || pf.At != at {
		return fmt.Errorf("hydrate: resume selection mismatch: in-progress restore is generation %d (at %q), requested generation %d (at %q) — use the same selection or a fresh dir",
			pf.Generation, pf.At, gen, at)
	}
	return nil
}

func (p *hydrateProgress) load() error {
	if !p.enabled {
		return nil
	}
	b, err := os.ReadFile(p.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hydrate: read progress: %w", err)
	}
	var pf progressFile
	if err := json.Unmarshal(b, &pf); err != nil {
		// Legacy shape: bare []string.
		var phases []string
		if err2 := json.Unmarshal(b, &phases); err2 != nil {
			return fmt.Errorf("hydrate: parse progress: %w", err)
		}
		for _, ph := range phases {
			p.Done[ph] = true
		}
		return nil
	}
	for _, ph := range pf.Phases {
		p.Done[ph] = true
	}
	return nil
}

func (p *hydrateProgress) complete(phase string) error {
	p.Done[phase] = true
	if !p.enabled {
		return nil
	}
	var phases []string
	for ph := range p.Done {
		phases = append(phases, ph)
	}
	sort.Strings(phases)
	pf := progressFile{Generation: p.generation, At: p.at, Phases: phases}
	b, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("hydrate: write progress: %w", err)
	}
	return os.Rename(tmp, p.path)
}

// doneBefore short-circuits a completed phase on resume.
func (p *hydrateProgress) alreadyDone(phase string) bool {
	return p.Done[phase]
}

// confineRelPath rejects absolute paths and any .. escape.
func confineRelPath(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("absolute path")
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("backslash in path (workspace paths are slash-only)")
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("escapes the restore directory")
	}
	return nil
}

// downloadVerified GETs key and checks its sha256 (per-class mismatch rule
// is the caller's; this always reports which object failed).
func downloadVerified(ctx context.Context, client *s3.Client, bucket, key, wantSHA string) ([]byte, error) {
	b, err := client.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	if wantSHA != "" {
		if got := sha256HexBytes(b); got != wantSHA {
			return nil, fmt.Errorf("sha256 mismatch on %s: state says %s, object hashes to %s", key, wantSHA, got)
		}
	}
	return b, nil
}

// restorePoint is the selected generation + restore chain.
type restorePoint struct {
	generation   int
	snapshot     string
	snapshotSHA  string
	wal          []WALSegmentRef
	objects      map[string]ObjectRef
	vectors      map[string]ObjectRef
	overshoot    int
	fromMeta     bool   // objects/vectors came from the selected generation's sealed meta map
	objectSkew   bool   // T predates the selected generation's seal (docs newer than T)
	fallbackNote string // set when the selected meta has no maps (old mirror)
}

func selectRestorePoint(ctx context.Context, client *s3.Client, prefix, bucket string, st *State, opts HydrateOpts) (*restorePoint, error) {
	if opts.Generation > 0 && !opts.At.IsZero() {
		return nil, fmt.Errorf("hydrate: --generation and --at are mutually exclusive (pick one restore point)")
	}
	// Explicit generation: live state for the live gen, meta.json otherwise.
	if opts.Generation > 0 {
		if opts.Generation == st.Generation {
			return &restorePoint{generation: st.Generation, snapshot: st.DB.Snapshot, snapshotSHA: st.DB.SnapshotSHA256, wal: st.DB.WAL, objects: st.Objects, vectors: st.Vectors}, nil
		}
		if opts.Generation > st.Generation {
			return nil, fmt.Errorf("hydrate: generation %d does not exist (newest is %d)", opts.Generation, st.Generation)
		}
		meta, err := loadGenerationMeta(ctx, client, prefix, bucket, opts.Generation)
		if err != nil {
			return nil, err
		}
		rp := &restorePoint{generation: meta.Generation, snapshot: meta.Snapshot, snapshotSHA: meta.SnapshotSHA256, wal: meta.WAL, objects: st.Objects, vectors: st.Vectors}
		if meta.Objects != nil || meta.Vectors != nil {
			rp.objects = meta.Objects
			rp.vectors = meta.Vectors
			rp.fromMeta = true
		} else {
			rp.fallbackNote = "note: generation has no object map; docs restored at newest"
		}
		return rp, nil
	}

	// Point-in-time: segment-granular, sealed_at ≤ TIME (spec.md §AC-6).
	if !opts.At.IsZero() {
		at := opts.At.UTC()
		if !at.Before(st.DB.CreatedAt.UTC()) {
			// Live generation: filter segments by sealed_at.
			var wal []WALSegmentRef
			var overshoot int
			for _, seg := range st.DB.WAL {
				if !seg.SealedAt.UTC().After(at) {
					wal = append(wal, seg)
				} else {
					overshoot++
				}
			}
			return &restorePoint{generation: st.Generation, snapshot: st.DB.Snapshot, snapshotSHA: st.DB.SnapshotSHA256, wal: wal, objects: st.Objects, vectors: st.Vectors, overshoot: overshoot}, nil
		}
		// Rotated generations: newest meta with created_at ≤ TIME.
		metas, err := listGenerationMetas(ctx, client, prefix, bucket, st.Generation)
		if err != nil {
			return nil, err
		}
		var best *GenerationMeta
		for _, meta := range metas {
			if !meta.CreatedAt.UTC().After(at) && (best == nil || meta.Generation > best.Generation) {
				best = meta
			}
		}
		if best == nil {
			return nil, fmt.Errorf("hydrate: %s predates the oldest restorable point (%s)", at.Format(time.RFC3339), oldestPoint(st, metas))
		}
		var wal []WALSegmentRef
		var overshoot int
		for _, seg := range best.WAL {
			if !seg.SealedAt.UTC().After(at) {
				wal = append(wal, seg)
			} else {
				overshoot++
			}
		}
		rp := &restorePoint{generation: best.Generation, snapshot: best.Snapshot, snapshotSHA: best.SnapshotSHA256, wal: wal, objects: st.Objects, vectors: st.Vectors, overshoot: overshoot}
		if best.Objects != nil || best.Vectors != nil {
			rp.objects = best.Objects
			rp.vectors = best.Vectors
			rp.fromMeta = true
			// Object skew is independent of segment count: the map is at the
			// generation's seal; the db is at T.
			if at.Before(best.SealedAt.UTC()) {
				rp.objectSkew = true
			}
		} else {
			rp.fallbackNote = "note: generation has no object map; docs restored at newest"
		}
		return rp, nil
	}

	// Newest.
	return &restorePoint{generation: st.Generation, snapshot: st.DB.Snapshot, snapshotSHA: st.DB.SnapshotSHA256, wal: st.DB.WAL, objects: st.Objects, vectors: st.Vectors}, nil
}

func oldestPoint(st *State, metas []*GenerationMeta) string {
	oldest := st.DB.CreatedAt
	for _, m := range metas {
		if m.CreatedAt.Before(oldest) {
			oldest = m.CreatedAt
		}
	}
	return oldest.UTC().Format(time.RFC3339)
}

func loadGenerationMeta(ctx context.Context, client *s3.Client, prefix, bucket string, gen int) (*GenerationMeta, error) {
	mb, err := client.GetObject(ctx, bucket, GenerationMetaKey(prefix, gen))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return nil, fmt.Errorf("hydrate: generation %d not found (no meta.json)", gen)
		}
		return nil, fmt.Errorf("hydrate: read meta gen %d: %w", gen, err)
	}
	meta, err := UnmarshalMeta(mb)
	if err != nil {
		return nil, err
	}
	if err := meta.Validate(); err != nil {
		return nil, fmt.Errorf("hydrate: meta gen %d invalid: %w", gen, err)
	}
	return meta, nil
}

// listGenerationMetas loads every meta.json under db/ older than live.
func listGenerationMetas(ctx context.Context, client *s3.Client, prefix, bucket string, liveGen int) ([]*GenerationMeta, error) {
	keys, err := client.ListObjects(ctx, bucket, prefix+"db/")
	if err != nil {
		return nil, fmt.Errorf("hydrate: list db/: %w", err)
	}
	var metas []*GenerationMeta
	for _, k := range keys {
		gen, err := ParseGenerationMetaKey(k)
		if err != nil || gen >= liveGen {
			continue
		}
		meta, err := loadGenerationMeta(ctx, client, prefix, bucket, gen)
		if err != nil {
			return nil, err
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Generation < metas[j].Generation })
	return metas, nil
}
