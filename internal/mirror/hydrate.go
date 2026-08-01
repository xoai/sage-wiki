package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	creds, err := ResolveCredentials(cfg.AccessKeyEnv, cfg.SecretKeyEnv, cfg.CredentialsFile)
	if err != nil {
		return nil, err
	}
	client, err := s3.NewClient(cfg.Endpoint, cfg.Region, creds, s3.WithPathStyle(true))
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

	// Empty-dir rule (no merge semantics).
	if entries, err := os.ReadDir(dst); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("hydrate: %s is not empty (restore requires an empty dir)", dst)
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
	rep.Generation = sel.generation
	if sel.overshoot > 0 {
		rep.Overshoot = fmt.Sprintf("%d segment(s) sealed after the requested time excluded", sel.overshoot)
	}

	if err := os.MkdirAll(filepath.Join(dst, ".sage"), 0o755); err != nil {
		return nil, err
	}

	// --- Phase: db (snapshot + WAL replay) ---
	dbBytes, err := downloadVerified(ctx, client, bucket, sel.snapshot, sel.snapshotSHA)
	if err != nil {
		return nil, err
	}
	dbRaw, err := zstdDecode(dbBytes)
	if err != nil {
		return nil, fmt.Errorf("hydrate: decompress snapshot: %w", err)
	}
	dbPath := filepath.Join(dst, ".sage", "wiki.db")
	if err := os.WriteFile(dbPath, dbRaw, 0o644); err != nil {
		return nil, fmt.Errorf("hydrate: write db: %w", err)
	}
	// WAL replay: concatenate segment bytes (seq order; seq 1 carries the
	// 32-byte header; later segments are frame ranges).
	if len(sel.wal) > 0 {
		var walBytes []byte
		for _, seg := range sel.wal {
			b, err := downloadVerified(ctx, client, bucket, seg.Key, seg.SHA256)
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

	// --- Phase: markdown/docs (skip tombstones; per-class rule: abort on
	// mismatch naming the object) ---
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
		b, err := downloadVerified(ctx, client, bucket, ref.Key, ref.SHA256)
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

	// --- Phase: vectors (warn + continue — rebuildable) ---
	vecNames := make([]string, 0, len(sel.vectors))
	for n := range sel.vectors {
		vecNames = append(vecNames, n)
	}
	sort.Strings(vecNames)
	for _, name := range vecNames {
		ref := sel.vectors[name]
		if ref.Deleted {
			continue
		}
		b, err := downloadVerified(ctx, client, bucket, ref.Key, ref.SHA256)
		if err != nil {
			rep.Advisories = append(rep.Advisories, fmt.Sprintf("vector %s not restored (rebuild with `index rebuild-vectors`): %v", name, err))
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, ".sage", name), b, 0o644); err != nil {
			return nil, fmt.Errorf("hydrate: write vector %s: %w", name, err)
		}
		rep.Checked++
	}

	rep.Valid = true
	rep.RestoredTo = dst
	return rep, nil
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
	generation  int
	snapshot    string
	snapshotSHA string
	wal         []WALSegmentRef
	objects     map[string]ObjectRef
	vectors     map[string]ObjectRef
	overshoot   int
}

func selectRestorePoint(ctx context.Context, client *s3.Client, prefix, bucket string, st *State, opts HydrateOpts) (*restorePoint, error) {
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
		return &restorePoint{generation: meta.Generation, snapshot: meta.Snapshot, snapshotSHA: meta.SnapshotSHA256, wal: meta.WAL, objects: st.Objects, vectors: st.Vectors}, nil
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
		return &restorePoint{generation: best.Generation, snapshot: best.Snapshot, snapshotSHA: best.SnapshotSHA256, wal: wal, objects: st.Objects, vectors: st.Vectors, overshoot: overshoot}, nil
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
