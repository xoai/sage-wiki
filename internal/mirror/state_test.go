package mirror

import (
	"strings"
	"testing"
	"time"
)

var stateTestTime = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func fixtureState() *State {
	sha := strings.Repeat("ab", 32)
	return &State{
		FormatVersion: FormatVersion,
		Generation:    3,
		DB: DBState{
			Snapshot:       "ws/db/generation-3/snapshot.db.zst",
			SnapshotSHA256: sha,
			CreatedAt:      stateTestTime,
			WAL: []WALSegmentRef{
				{Key: "ws/db/generation-3/wal/000001.zst", SHA256: sha, SealedAt: stateTestTime},
			},
		},
		Objects: map[string]ObjectRef{
			"wiki/concepts/Foo.md": {Key: "ws/objects/docs/ab/" + sha, SHA256: sha},
			"raw/paper.pdf":        {Key: "ws/objects/docs/cd/" + strings.Repeat("cd", 32), SHA256: strings.Repeat("cd", 32), Deleted: true},
		},
		Vectors: map[string]ObjectRef{
			"vectors.idx": {Key: "ws/vectors/" + sha, SHA256: sha},
		},
		UpdatedAt: stateTestTime.Add(time.Hour),
	}
}

// TestStateMarshal_Deterministic pins byte-identical output across runs and
// adversarial map insertion orders (ground rule 6).
func TestStateMarshal_Deterministic(t *testing.T) {
	s1 := fixtureState()
	b1, err := MarshalState(s1)
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	// Rebuild the same state with reversed insertion order.
	s2 := fixtureState()
	s2.Objects = map[string]ObjectRef{}
	objs := fixtureState().Objects
	s2.Objects["raw/paper.pdf"] = objs["raw/paper.pdf"]
	s2.Objects["wiki/concepts/Foo.md"] = objs["wiki/concepts/Foo.md"]
	b2, err := MarshalState(s2)
	if err != nil {
		t.Fatalf("MarshalState s2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic marshal:\n%s\n---\n%s", b1, b2)
	}
	// Golden shape spot-checks: sorted keys, RFC3339 UTC, format version.
	str := string(b1)
	if !strings.Contains(str, `"format_version": 1`) {
		t.Fatal("missing format_version")
	}
	if strings.Index(str, "raw/paper.pdf") > strings.Index(str, "wiki/concepts/Foo.md") {
		t.Fatal("object keys not sorted")
	}
	if !strings.Contains(str, `"created_at": "2026-08-01T12:00:00Z"`) {
		t.Fatal("timestamp not RFC3339 UTC")
	}
}

func TestStateRoundTrip(t *testing.T) {
	b, _ := MarshalState(fixtureState())
	got, err := UnmarshalState(b)
	if err != nil {
		t.Fatalf("UnmarshalState: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate after round-trip: %v", err)
	}
	if got.Generation != 3 || len(got.DB.WAL) != 1 || len(got.Objects) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !got.Objects["raw/paper.pdf"].Deleted {
		t.Fatal("tombstone lost")
	}
}

func TestStateValidate(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	cases := []struct {
		name   string
		mutate func(*State)
	}{
		{"bad version", func(s *State) { s.FormatVersion = 2 }},
		{"zero generation", func(s *State) { s.Generation = 0 }},
		{"empty snapshot key", func(s *State) { s.DB.Snapshot = "" }},
		{"snapshot/generation mismatch", func(s *State) { s.DB.Snapshot = "ws/db/generation-9/snapshot.db.zst" }},
		{"bad snapshot sha", func(s *State) { s.DB.SnapshotSHA256 = "zz" }},
		{"wal sha bad", func(s *State) { s.DB.WAL[0].SHA256 = "x" }},
		{"wal/generation mismatch", func(s *State) { s.DB.WAL[0].Key = "ws/db/generation-2/wal/000001.zst" }},
		{"object key not content-addressed", func(s *State) {
			s.Objects["wiki/x.md"] = ObjectRef{Key: "ws/objects/docs/xx/nope", SHA256: sha}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureState() // fresh copy per case (slice/map aliasing)
			tc.mutate(s)
			if err := s.Validate(); err == nil {
				t.Fatal("Validate should fail")
			}
		})
	}
	if err := fixtureState().Validate(); err != nil {
		t.Fatalf("fixture should validate: %v", err)
	}
}

func TestGenerationMeta_RoundTripAndValidate(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	meta := &GenerationMeta{
		FormatVersion:  FormatVersion,
		Generation:     3,
		CreatedAt:      stateTestTime,
		SealedAt:       stateTestTime.Add(2 * time.Hour),
		Snapshot:       "ws/db/generation-3/snapshot.db.zst",
		SnapshotSHA256: sha,
		WAL: []WALSegmentRef{
			{Key: "ws/db/generation-3/wal/000001.zst", SHA256: sha, SealedAt: stateTestTime},
		},
	}
	b, err := MarshalMeta(meta)
	if err != nil {
		t.Fatalf("MarshalMeta: %v", err)
	}
	got, err := UnmarshalMeta(b)
	if err != nil {
		t.Fatalf("UnmarshalMeta: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("meta Validate: %v", err)
	}
	if got.Generation != 3 || got.SealedAt.IsZero() {
		t.Fatalf("meta round-trip mismatch: %+v", got)
	}
}

func TestStateTimestampsUTC(t *testing.T) {
	// Non-UTC input must normalize to UTC on marshal (determinism).
	s := fixtureState()
	loc := time.FixedZone("UTC+7", 7*3600)
	s.UpdatedAt = stateTestTime.In(loc)
	b, _ := MarshalState(s)
	if !strings.Contains(string(b), `"updated_at": "2026-08-01T12:00:00Z"`) {
		t.Fatalf("non-UTC timestamp leaked: %s", b)
	}
}

// GenerationMeta object maps (per-generation PITR): additive, validated
// with the same rules as mirror-state when present.
func TestGenerationMeta_MapsRoundTrip(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: stateTestTime, SealedAt: stateTestTime.Add(time.Hour),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha,
		WAL: []WALSegmentRef{},
		Objects: map[string]ObjectRef{
			"wiki/concepts/Foo.md": {Key: "ws/objects/docs/ab/" + sha, SHA256: sha},
		},
		Vectors: map[string]ObjectRef{
			"vectors.idx": {Key: "ws/vectors/" + sha, SHA256: sha},
		},
	}
	b1, err := MarshalMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic across insertion order (≥2 entries, reversed — a
	// single-entry map cannot detect nondeterminism, review nit).
	meta.Objects["wiki/concepts/Bar.md"] = meta.Objects["wiki/concepts/Foo.md"]
	b1, err = MarshalMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	meta2 := &GenerationMeta{}
	*meta2 = *meta
	meta2.Objects = map[string]ObjectRef{}
	meta2.Objects["wiki/concepts/Bar.md"] = meta.Objects["wiki/concepts/Bar.md"]
	meta2.Objects["wiki/concepts/Foo.md"] = meta.Objects["wiki/concepts/Foo.md"]
	b2, err := MarshalMeta(meta2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("non-deterministic meta marshal")
	}
	got, err := UnmarshalMeta(b1)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("meta with maps must validate: %v", err)
	}
	if got.Objects["wiki/concepts/Foo.md"].SHA256 != sha {
		t.Fatal("object map lost in round-trip")
	}
}

func TestGenerationMeta_MapsAbsentIsValid(t *testing.T) {
	// Old pre-feature meta (no maps) must load + validate.
	sha := strings.Repeat("ab", 32)
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: stateTestTime, SealedAt: stateTestTime,
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha,
		WAL: []WALSegmentRef{},
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("absent maps must be tolerated: %v", err)
	}
}

func TestGenerationMeta_MapsValidatedWhenPresent(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	base := func() *GenerationMeta {
		return &GenerationMeta{
			FormatVersion: FormatVersion, Generation: 2,
			CreatedAt: stateTestTime, SealedAt: stateTestTime,
			Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha,
			WAL: []WALSegmentRef{},
		}
	}
	cases := []struct {
		name   string
		mutate func(*GenerationMeta)
	}{
		{"unsafe path", func(m *GenerationMeta) {
			m.Objects = map[string]ObjectRef{"../escape.md": {Key: "ws/objects/docs/ab/" + sha, SHA256: sha}}
		}},
		{"non-basename vector", func(m *GenerationMeta) {
			m.Vectors = map[string]ObjectRef{"../vec.idx": {Key: "ws/vectors/" + sha, SHA256: sha}}
		}},
		{"bad sha", func(m *GenerationMeta) {
			m.Objects = map[string]ObjectRef{"wiki/a.md": {Key: "ws/objects/docs/ab/" + sha, SHA256: "zz"}}
		}},
		{"non-content-addressed key", func(m *GenerationMeta) {
			m.Objects = map[string]ObjectRef{"wiki/a.md": {Key: "ws/objects/docs/xx/nope", SHA256: sha}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.mutate(m)
			if err := m.Validate(); err == nil {
				t.Fatal("invalid map entry must fail validation")
			}
		})
	}
}
