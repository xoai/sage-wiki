package mirror

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// ResolveCredentials resolves S3 credentials from the NAMED env vars or a
// credentials file (spec: env names win when both resolve; no implicit AWS
// default chain). The session token comes from the SAME source as the keys:
// tokenKeyEnv when keys come from env, "session_token" when from the file —
// cross-source pairing is a LOUD error (signing without a token yields
// opaque 403s). An empty token value reads as absent.
func ResolveCredentials(accessKeyEnv, secretKeyEnv, tokenKeyEnv, credentialsFile string) (s3.Credentials, error) {
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
		if credentialsFile != "" {
			// Cross-source check: a file carrying a session token while keys
			// come from env is a misconfiguration — fail loudly (F-024: an
			// UNREADABLE or malformed file must not bypass it silently).
			shape, err := readCredentialsShape(credentialsFile)
			if err != nil {
				return s3.Credentials{}, fmt.Errorf("mirror: credentials_file %s unreadable while env keys are set (%v) — pick ONE source", credentialsFile, err)
			}
			if shape.SessionToken != "" {
				return s3.Credentials{}, fmt.Errorf("mirror: credentials from env (%s/%s) but %s carries a session_token — pick ONE source (cross-source pairing is unsupported)", accessKeyEnv, secretKeyEnv, credentialsFile)
			}
		}
		var token string
		if tokenKeyEnv != "" {
			if v, ok := os.LookupEnv(tokenKeyEnv); ok {
				token = v // empty value = absent
			}
		}
		return s3.Credentials{AccessKey: ak, SecretKey: sk, SessionToken: token}, nil
	}
	if credentialsFile != "" {
		// Reverse cross-source (F-025): file keys + env token is the same
		// misconfiguration in the other direction — fail loudly too.
		if tokenKeyEnv != "" {
			if v, ok := os.LookupEnv(tokenKeyEnv); ok && v != "" {
				return s3.Credentials{}, fmt.Errorf("mirror: credentials from %s but %s is set — pick ONE source (cross-source pairing is unsupported)", credentialsFile, tokenKeyEnv)
			}
		}
		return readCredentialsFile(credentialsFile)
	}
	return s3.Credentials{}, fmt.Errorf("mirror: no credentials: set %s and %s, or configure credentials_file", accessKeyEnv, secretKeyEnv)
}

type credentialsFileShape struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token"`
}

func readCredentialsShape(path string) (credentialsFileShape, error) {
	var shape credentialsFileShape
	b, err := os.ReadFile(path)
	if err != nil {
		return shape, err
	}
	if err := json.Unmarshal(b, &shape); err != nil {
		return shape, err
	}
	return shape, nil
}

func firstSet(akEnv, skEnv string, akOK bool) string {
	if akOK {
		return akEnv
	}
	return skEnv
}

func readCredentialsFile(path string) (s3.Credentials, error) {
	shape, err := readCredentialsShape(path)
	if err != nil {
		return s3.Credentials{}, fmt.Errorf("mirror: read credentials_file: %w", err)
	}
	if shape.AccessKey == "" || shape.SecretKey == "" {
		return s3.Credentials{}, fmt.Errorf("mirror: credentials_file %s must contain access_key and secret_key", path)
	}
	return s3.Credentials{AccessKey: shape.AccessKey, SecretKey: shape.SecretKey, SessionToken: shape.SessionToken}, nil
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
