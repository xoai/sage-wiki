package mirror

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// ResolveCredentials resolves S3 credentials from the NAMED env vars or a
// credentials file (spec.md §Config: env names win when both resolve; no
// implicit AWS default chain — explicit and greppable).
func ResolveCredentials(accessKeyEnv, secretKeyEnv, credentialsFile string) (s3.Credentials, error) {
	ak, akOK := os.LookupEnv(accessKeyEnv)
	sk, skOK := os.LookupEnv(secretKeyEnv)
	if akOK != skOK {
		missing := secretKeyEnv
		if !akOK {
			missing = accessKeyEnv
		}
		return s3.Credentials{}, fmt.Errorf("mirror: partial credentials: %s is set but %s is not", firstSet(accessKeyEnv, secretKeyEnv, akOK), missing)
	}
	if akOK && skOK {
		if ak == "" || sk == "" {
			return s3.Credentials{}, fmt.Errorf("mirror: %s/%s are set but empty", accessKeyEnv, secretKeyEnv)
		}
		return s3.Credentials{AccessKey: ak, SecretKey: sk}, nil
	}
	if credentialsFile != "" {
		return readCredentialsFile(credentialsFile)
	}
	return s3.Credentials{}, fmt.Errorf("mirror: no credentials: set %s and %s, or configure credentials_file", accessKeyEnv, secretKeyEnv)
}

func firstSet(akEnv, skEnv string, akOK bool) string {
	if akOK {
		return akEnv
	}
	return skEnv
}

type credentialsFileShape struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

func readCredentialsFile(path string) (s3.Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return s3.Credentials{}, fmt.Errorf("mirror: read credentials_file: %w", err)
	}
	var shape credentialsFileShape
	if err := json.Unmarshal(b, &shape); err != nil {
		return s3.Credentials{}, fmt.Errorf("mirror: parse credentials_file %s: %w", path, err)
	}
	if shape.AccessKey == "" || shape.SecretKey == "" {
		return s3.Credentials{}, fmt.Errorf("mirror: credentials_file %s must contain access_key and secret_key", path)
	}
	return s3.Credentials{AccessKey: shape.AccessKey, SecretKey: shape.SecretKey}, nil
}

// LoadEncryptionKey reads a 32-byte AES-256 key file (must live OUTSIDE the
// workspace — enforced by the caller, which knows the workspace dir).
func LoadEncryptionKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("mirror: read encryption key_file: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("mirror: encryption key_file %s is %d bytes, want exactly 32 (AES-256)", path, len(b))
	}
	return b, nil
}
