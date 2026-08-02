package mirror

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// AES-256-GCM envelope (spec.md §Encryption): nonce(12) || ciphertext.
// Client-side, off by default; stdlib only (crypto/aes, crypto/cipher).

const gcmNonceSize = 12

// encryptBytes seals plaintext under a 32-byte key with a random nonce.
func encryptBytes(key, plaintext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("mirror: encryption key is %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mirror: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mirror: gcm: %w", err)
	}
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("mirror: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptBytes opens an envelope; a wrong key fails loudly (AEAD).
func decryptBytes(key, ciphertext []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("mirror: encryption key is %d bytes, want 32", len(key))
	}
	if len(ciphertext) < gcmNonceSize {
		return nil, fmt.Errorf("mirror: ciphertext shorter than nonce (%d bytes)", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("mirror: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("mirror: gcm: %w", err)
	}
	nonce, body := ciphertext[:gcmNonceSize], ciphertext[gcmNonceSize:]
	plain, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, fmt.Errorf("mirror: decrypt failed (wrong key or corrupted object): %w", err)
	}
	return plain, nil
}

// loadEncryptionKey loads the key when encryption is enabled (Open calls it).
func (m *Mirror) loadEncryptionKey() error {
	if !m.cfg.Encryption.Enabled {
		return nil
	}
	key, err := LoadEncryptionKey(m.cfg.Encryption.KeyFile)
	if err != nil {
		return err
	}
	m.encKey = key
	return nil
}

// shipBytes is the outbound byte transform (identity unless encrypted).
// Integrity sha in mirror-state is ALWAYS over these shipped bytes, so
// verify works without the key.
func (m *Mirror) shipBytes(b []byte) ([]byte, error) {
	if m.encKey == nil {
		return b, nil
	}
	return encryptBytes(m.encKey, b)
}
