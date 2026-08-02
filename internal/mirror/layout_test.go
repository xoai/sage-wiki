package mirror

import (
	"strings"
	"testing"
)

func TestLayoutKeys(t *testing.T) {
	sha := strings.Repeat("ab", 32) // 64 hex chars
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"manifest", ManifestKey("ws/"), "ws/manifest.json"},
		{"state", StateKey("ws/"), "ws/mirror-state.json"},
		{"doc", DocObjectKey("ws/", sha), "ws/objects/docs/ab/" + sha},
		{"vector", VectorObjectKey("ws/", sha), "ws/vectors/" + sha},
		{"snapshot", SnapshotKey("ws/", 3), "ws/db/generation-3/snapshot.db.zst"},
		{"segment", WALSegmentKey("ws/", 3, 7), "ws/db/generation-3/wal/000007.zst"},
		{"meta", GenerationMetaKey("ws/", 3), "ws/db/generation-3/meta.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestNormalizePrefix(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"ws":     "ws/",
		"ws/":    "ws/",
		"/ws/":   "ws/",
		"a/b/c":  "a/b/c/",
		"ws//":   "ws/",
		"   ":    "",
		"ws/sub": "ws/sub/",
	}
	for in, want := range cases {
		if got := NormalizePrefix(in); got != want {
			t.Fatalf("NormalizePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSegmentSeqPadding(t *testing.T) {
	if got := WALSegmentKey("", 1, 0); got != "db/generation-1/wal/000000.zst" {
		t.Fatalf("seq 0 = %q", got)
	}
	if got := WALSegmentKey("", 1, 999999); got != "db/generation-1/wal/999999.zst" {
		t.Fatalf("seq max = %q", got)
	}
}

func TestParseWALSegmentKey(t *testing.T) {
	gen, seq, err := ParseWALSegmentKey("ws/db/generation-12/wal/000042.zst")
	if err != nil || gen != 12 || seq != 42 {
		t.Fatalf("ParseWALSegmentKey = %d, %d, %v", gen, seq, err)
	}
	bad := []string{
		"ws/db/generation-12/wal/42.zst",      // unpadded
		"ws/db/generation-x/wal/000042.zst",   // bad gen
		"ws/db/generation-12/snapshot.db.zst", // not a segment
		"ws/objects/docs/ab/cd",               // wrong subtree
	}
	for _, k := range bad {
		if _, _, err := ParseWALSegmentKey(k); err == nil {
			t.Fatalf("ParseWALSegmentKey(%q) should fail", k)
		}
	}
}

func TestParseGenerationFromMetaKey(t *testing.T) {
	gen, err := ParseGenerationMetaKey("ws/db/generation-5/meta.json")
	if err != nil || gen != 5 {
		t.Fatalf("ParseGenerationMetaKey = %d, %v", gen, err)
	}
	if _, err := ParseGenerationMetaKey("ws/db/generation-5/snapshot.db.zst"); err == nil {
		t.Fatal("snapshot key should not parse as meta key")
	}
}

func TestValidateSHA256Hex(t *testing.T) {
	if !ValidSHA256Hex(strings.Repeat("ab", 32)) {
		t.Fatal("valid sha256 rejected")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("AB", 32), strings.Repeat("zz", 32), strings.Repeat("ab", 33)} {
		if ValidSHA256Hex(bad) {
			t.Fatalf("invalid sha256 accepted: %q", bad)
		}
	}
}

func TestDocObjectKeyRejectsBadSHA(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on bad sha (programmer error guard)")
		}
	}()
	DocObjectKey("ws/", "not-a-sha")
}
