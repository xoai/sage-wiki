package mirror

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func writeKeyFile(t *testing.T) string {
	return writeKeyFileVariant(t, 7)
}

func writeKeyFileVariant(t *testing.T, mult byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mirror.key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(int(key[i]) + i*int(mult))
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func encryptedShipFixture(t *testing.T) *shipFixture {
	t.Helper()
	f := newShipFixture(t)
	defer f.dbClose()
	keyPath := writeKeyFile(t)
	f.m.cfg.Encryption = EncryptionConfig{Enabled: true, KeyFile: keyPath}
	if err := f.m.loadEncryptionKey(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEncrypt_RoundTrip(t *testing.T) {
	f := encryptedShipFixture(t)
	// Re-enable with encryption (fixture enabled unencrypted first — build
	// a fresh encrypted workspace instead).
	_ = f
	keyPath := writeKeyFile(t)
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	cfg.Encryption = EncryptionConfig{Enabled: true, KeyFile: keyPath}
	dir := makeWorkspaceWithDB(t)
	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	// Manifest marked encrypted.
	mb, _ := fake.get("ws/manifest.json")
	if !strings.Contains(string(mb), `"encrypted": true`) {
		t.Fatal("manifest not marked encrypted")
	}
	// Shipped bytes are NOT plaintext db.
	st := remoteStateFromFake(t, fake)
	snapBytes, _ := fake.get(st.DB.Snapshot)
	if strings.Contains(string(snapBytes), "SQLite format 3") {
		t.Fatal("snapshot shipped in plaintext despite encryption")
	}
	// Verify works WITHOUT the key (sha over shipped bytes).
	rep, err := m.Verify(context.Background())
	if err != nil || !rep.Valid {
		t.Fatalf("verify on encrypted mirror: %+v %v", rep, err)
	}
	// Ship objects + docs under encryption.
	writeWS(t, dir, "wiki/concepts/Secret.md", "top secret content")
	if _, err := m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	for key, b := range fake.objects {
		if strings.Contains(string(b), "top secret content") {
			t.Fatalf("plaintext leaked in object %s", key)
		}
	}
	// Hydrate WITH the key → plaintext back.
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), cfgWithKey(cfg, keyPath), dst, HydrateOpts{KeyFile: keyPath}); err != nil {
		t.Fatalf("hydrate with key: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "wiki/concepts/Secret.md"))
	if err != nil || string(b) != "top secret content" {
		t.Fatalf("restored content = %q, %v", b, err)
	}
	// Hydrate with WRONG key → loud failure.
	badKey := writeKeyFileVariant(t, 13)
	dst2 := filepath.Join(t.TempDir(), "restored2")
	if _, err := Hydrate(context.Background(), cfgWithKey(cfg, badKey), dst2, HydrateOpts{KeyFile: badKey}); err == nil {
		t.Fatal("wrong key must fail loudly")
	}
}

func cfgWithKey(cfg Config, keyPath string) Config {
	cfg.Encryption = EncryptionConfig{Enabled: true, KeyFile: keyPath}
	return cfg
}

func remoteStateFromFake(t *testing.T, fake *fakeS3) *State {
	t.Helper()
	sb, ok := fake.get("ws/mirror-state.json")
	if !ok {
		t.Fatal("no remote state")
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestEncrypt_NonceUniqueness(t *testing.T) {
	key := make([]byte, 32)
	c1, err := encryptBytes(key, []byte("same plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	c2, err := encryptBytes(key, []byte("same plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	if string(c1) == string(c2) {
		t.Fatal("nonce reuse: identical plaintext produced identical ciphertext")
	}
	back, err := decryptBytes(key, c1)
	if err != nil || string(back) != "same plaintext" {
		t.Fatalf("decrypt = %q, %v", back, err)
	}
}

func TestEncrypt_DiffDoesNotReshipEverything(t *testing.T) {
	keyPath := writeKeyFile(t)
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	cfg.Encryption = EncryptionConfig{Enabled: true, KeyFile: keyPath}
	dir := makeWorkspaceWithDB(t)
	m, _ := Open(dir, cfg, NewDiffChangeSource(dir))
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo")
	if _, err := m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	putsBefore := countPuts(fake)
	res, err := m.shipPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.ObjectsShipped != 0 || countPuts(fake) != putsBefore {
		t.Fatal("encryption broke diff dedupe — everything re-shipped")
	}
}

func countPuts(fake *fakeS3) int { return len(fake.putLog) }

var _ = sql.Open
