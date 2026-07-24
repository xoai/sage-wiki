package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
)

type storeFile struct {
	Version             int                    `json:"version"`
	TOSAcknowledged     bool                   `json:"tos_acknowledged,omitempty"`
	KeychainNoticeShown bool                   `json:"keychain_notice_shown,omitempty"`
	Credentials         map[string]*Credential `json:"credentials,omitempty"`
}

type Store struct {
	path string
	// backend/kr are the test seam for backend selection (P2-6): empty
	// means "probe at first use" (production path); tests set them
	// directly and never touch a real keychain.
	backend string
	kr      keyringAPI
}

func NewStore(path string) *Store {
	s := &Store{path: path}
	s.maybeKeychainNotice()
	return s
}

// NewStoreNoNotice constructs without the first-run notice (used by
// `auth migrate` itself — the notice would be self-referential).
func NewStoreNoNotice(path string) *Store {
	return &Store{path: path}
}

func DefaultStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".sage-wiki", "auth.json")
}

func (s *Store) ensureDir() error {
	dir := filepath.Dir(s.path)
	return os.MkdirAll(dir, 0700)
}

func (s *Store) read() (*storeFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &storeFile{Version: 1, Credentials: make(map[string]*Credential)}, nil
		}
		return nil, fmt.Errorf("auth: read %s: %w", s.path, err)
	}

	s.checkPermissions()

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("auth: parse %s: %w", s.path, err)
	}

	if sf.Version != 0 && sf.Version != 1 {
		return nil, fmt.Errorf("auth: unrecognized auth.json format (version %d). Delete %s and re-authenticate with sage-wiki auth login", sf.Version, s.path)
	}
	if sf.Version == 0 {
		sf.Version = 1
	}
	if sf.Credentials == nil {
		sf.Credentials = make(map[string]*Credential)
	}

	for name, cred := range sf.Credentials {
		cred.Provider = name
	}

	return &sf, nil
}

func (s *Store) write(sf *storeFile) error {
	if err := s.ensureDir(); err != nil {
		return fmt.Errorf("auth: create dir: %w", err)
	}

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("auth: marshal: %w", err)
	}

	return os.WriteFile(s.path, data, 0600)
}

func (s *Store) checkPermissions() {
	info, err := os.Stat(s.path)
	if err != nil {
		return
	}
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		log.Warn("auth.json has insecure permissions", "path", s.path, "mode", fmt.Sprintf("%04o", mode),
			"fix", fmt.Sprintf("chmod 600 %s", s.path))
	}
}

func (s *Store) Get(provider string) (*Credential, error) {
	return s.getWithBackend(provider)
}

func (s *Store) Put(provider string, cred *Credential) error {
	return s.putWithBackend(provider, cred)
}

func (s *Store) Delete(provider string) error {
	return s.deleteWithBackend(provider)
}

func (s *Store) List() (map[string]*Credential, error) {
	return s.listWithBackend()
}

func (s *Store) IsTOSAcknowledged() bool {
	sf, err := s.read()
	if err != nil {
		return false
	}
	return sf.TOSAcknowledged
}

func (s *Store) AcknowledgeTOS() error {
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()

	sf, err := s.read()
	if err != nil {
		return err
	}

	sf.TOSAcknowledged = true
	return s.write(sf)
}
