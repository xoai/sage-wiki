package mirror

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// FormatVersion is the public mirror layout version (spec.md §Data model).
const FormatVersion = 1

// WALSegmentRef is one sealed WAL segment committed to the bucket.
type WALSegmentRef struct {
	Key      string    `json:"key"`
	SHA256   string    `json:"sha256"`
	SealedAt time.Time `json:"sealed_at"`
}

// DBState is the live generation's database restore chain.
type DBState struct {
	Snapshot       string          `json:"snapshot"`
	SnapshotSHA256 string          `json:"snapshot_sha256"`
	CreatedAt      time.Time       `json:"created_at"`
	WAL            []WALSegmentRef `json:"wal"`
}

// ObjectRef is a content-addressed shipped object (Deleted = tombstone).
// SHA256 is over the SHIPPED bytes (ciphertext when encrypted — verify
// works without the key). ContentSHA256 is the plaintext content hash,
// set only under encryption: diff dedupe and key addressing use it,
// because ciphertext is nonce-random per put.
type ObjectRef struct {
	Key           string `json:"key"`
	SHA256        string `json:"sha256"`
	Deleted       bool   `json:"deleted"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

// State is mirror-state.json — the single commit pointer, always written
// LAST after every object it references is durably uploaded.
type State struct {
	FormatVersion     int                  `json:"format_version"`
	Generation        int                  `json:"generation"`
	DB                DBState              `json:"db"`
	Objects           map[string]ObjectRef `json:"objects"`
	Vectors           map[string]ObjectRef `json:"vectors"`
	UpdatedAt         time.Time            `json:"updated_at"`
	RetainGenerations int                  `json:"retain_generations,omitempty"` // shipper's retention (verify prefers this over local config; additive)
}

// GenerationMeta is db/generation-N/meta.json — the rotation record of a
// superseded generation, derived from mirror-state by construction. Objects
// and Vectors seal the committed object maps at rotation (per-generation
// PITR — additive; old metas lack them and hydrate falls back).
type GenerationMeta struct {
	FormatVersion  int                  `json:"format_version"`
	Generation     int                  `json:"generation"`
	CreatedAt      time.Time            `json:"created_at"`
	SealedAt       time.Time            `json:"sealed_at"`
	Snapshot       string               `json:"snapshot"`
	SnapshotSHA256 string               `json:"snapshot_sha256"`
	WAL            []WALSegmentRef      `json:"wal"`
	Objects        map[string]ObjectRef `json:"objects"`
	Vectors        map[string]ObjectRef `json:"vectors"`
}

// MarshalState serializes deterministically: encoding/json sorts map keys;
// timestamps are forced to UTC RFC3339 (never RFC3339Nano — fractional
// seconds trim trailing zeros and break byte equality).
func MarshalState(s *State) ([]byte, error) {
	s.UpdatedAt = s.UpdatedAt.UTC()
	s.DB.CreatedAt = s.DB.CreatedAt.UTC()
	for i := range s.DB.WAL {
		s.DB.WAL[i].SealedAt = s.DB.WAL[i].SealedAt.UTC()
	}
	return json.MarshalIndent(s, "", "  ")
}

// UnmarshalState parses mirror-state.json bytes.
func UnmarshalState(b []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("mirror-state: parse: %w", err)
	}
	return &s, nil
}

// MarshalMeta serializes a rotation record (same determinism rules).
func MarshalMeta(m *GenerationMeta) ([]byte, error) {
	m.CreatedAt = m.CreatedAt.UTC()
	m.SealedAt = m.SealedAt.UTC()
	for i := range m.WAL {
		m.WAL[i].SealedAt = m.WAL[i].SealedAt.UTC()
	}
	return json.MarshalIndent(m, "", "  ")
}

// UnmarshalMeta parses a generation meta.json.
func UnmarshalMeta(b []byte) (*GenerationMeta, error) {
	var m GenerationMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("generation meta: parse: %w", err)
	}
	return &m, nil
}

// Validate checks the structural invariants of a State (format version,
// generation coherence, content-addressed keys, sha shapes).
func (s *State) Validate() error {
	if s.FormatVersion != FormatVersion {
		return fmt.Errorf("mirror-state: format_version %d, want %d", s.FormatVersion, FormatVersion)
	}
	if s.Generation < 1 {
		return fmt.Errorf("mirror-state: generation %d < 1", s.Generation)
	}
	if s.DB.CreatedAt.IsZero() {
		return fmt.Errorf("mirror-state: db.created_at missing (F-116)")
	}
	return validateDB("mirror-state", s.Generation, s.DB.Snapshot, s.DB.SnapshotSHA256, s.DB.WAL, s.Objects, s.Vectors)
}

// Validate checks a rotation record.
func (m *GenerationMeta) Validate() error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("meta: format_version %d, want %d", m.FormatVersion, FormatVersion)
	}
	if m.Generation < 1 {
		return fmt.Errorf("meta: generation %d < 1", m.Generation)
	}
	if m.CreatedAt.IsZero() {
		return fmt.Errorf("meta: created_at missing (F-116)")
	}
	if m.SealedAt.IsZero() {
		return fmt.Errorf("meta: sealed_at missing")
	}
	if err := validateDB("meta", m.Generation, m.Snapshot, m.SnapshotSHA256, m.WAL, nil, nil); err != nil {
		return err
	}
	// Object maps: absent = tolerated (pre-feature metas); present = the
	// same rules as mirror-state (paths confined, vectors basename,
	// content-addressed keys, sha shapes).
	for path, ref := range m.Objects {
		if err := confineRelPath(path); err != nil {
			return fmt.Errorf("meta objects[%q]: unsafe path: %w", path, err)
		}
		if err := validateObjectRef("meta objects", path, ref); err != nil {
			return err
		}
	}
	for name, ref := range m.Vectors {
		if name != filepath.Base(name) {
			return fmt.Errorf("meta vectors[%q]: not a basename (unsafe)", name)
		}
		if err := validateObjectRef("meta vectors", name, ref); err != nil {
			return err
		}
	}
	return nil
}

func validateDB(what string, gen int, snapshot, snapshotSHA string, wal []WALSegmentRef, objects, vectors map[string]ObjectRef) error {
	wantSnapshot := SnapshotKey("", gen)
	if snapshot == "" {
		return fmt.Errorf("%s: snapshot key missing", what)
	}
	if len(snapshot) < len(wantSnapshot) || snapshot[len(snapshot)-len(wantSnapshot):] != wantSnapshot {
		return fmt.Errorf("%s: snapshot key %q does not match generation %d", what, snapshot, gen)
	}
	if !ValidSHA256Hex(snapshotSHA) {
		return fmt.Errorf("%s: snapshot_sha256 invalid", what)
	}
	for i, seg := range wal {
		g, seq, err := ParseWALSegmentKey(seg.Key)
		if err != nil {
			return fmt.Errorf("%s: wal[%d]: %w", what, i, err)
		}
		if g != gen {
			return fmt.Errorf("%s: wal[%d] key belongs to generation %d, want %d", what, i, g, gen)
		}
		if i == 0 && seq != 1 {
			// F-106: the chain's first segment must be seq 1 (it carries the
			// WAL header; a headerless first segment replays as garbage).
			return fmt.Errorf("%s: wal[0] seq %06d, want 000001", what, seq)
		}
		if i > 0 {
			_, prevSeq, _ := ParseWALSegmentKey(wal[i-1].Key)
			if seq != prevSeq+1 {
				return fmt.Errorf("%s: wal seq gap: %06d after %06d", what, seq, prevSeq)
			}
		}
		if !ValidSHA256Hex(seg.SHA256) {
			return fmt.Errorf("%s: wal[%d] sha256 invalid", what, i)
		}
		if seg.SealedAt.IsZero() {
			return fmt.Errorf("%s: wal[%d] sealed_at missing", what, i)
		}
	}
	for path, ref := range objects {
		if err := confineRelPath(path); err != nil {
			return fmt.Errorf("objects[%q]: unsafe path: %w", path, err)
		}
		if err := validateObjectRef("objects", path, ref); err != nil {
			return err
		}
	}
	for name, ref := range vectors {
		if name != filepath.Base(name) {
			return fmt.Errorf("vectors[%q]: not a basename (unsafe)", name)
		}
		if err := validateObjectRef("vectors", name, ref); err != nil {
			return err
		}
	}
	return nil
}

func validateObjectRef(kind, name string, ref ObjectRef) error {
	if ref.Key == "" {
		return fmt.Errorf("%s[%q]: key missing", kind, name)
	}
	if !ValidSHA256Hex(ref.SHA256) {
		return fmt.Errorf("%s[%q]: sha256 invalid", kind, name)
	}
	// Content-addressed: the key must END with the content sha (docs:
	// .../<sha2>/<sha>, vectors: .../<sha>) — the CONTENT hash when
	// encrypted, else the shipped-bytes hash. Tombstones keep it too.
	addrSHA := ref.SHA256
	if ref.ContentSHA256 != "" {
		addrSHA = ref.ContentSHA256
	}
	if len(ref.Key) < 64 || ref.Key[len(ref.Key)-64:] != addrSHA {
		return fmt.Errorf("%s[%q]: key %q is not content-addressed by its content sha256", kind, name, ref.Key)
	}
	return nil
}

// SortedObjectPaths returns the committed object paths in deterministic
// order — every serialized/derived output sorts before writing.
func (s *State) SortedObjectPaths() []string {
	paths := make([]string, 0, len(s.Objects))
	for p := range s.Objects {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}
