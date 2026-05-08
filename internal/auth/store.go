package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
)

type storeFile struct {
	Version     int                    `json:"version"`
	TOSAcknowledged bool              `json:"tos_acknowledged,omitempty"`
	Credentials map[string]*Credential `json:"credentials,omitempty"`
}

type Store struct {
	path string
}

func NewStore(path string) *Store {
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
	sf, err := s.read()
	if err != nil {
		return nil, err
	}
	cred, ok := sf.Credentials[provider]
	if !ok {
		return nil, fmt.Errorf("auth: no credentials for provider %q", provider)
	}
	return cred, nil
}

func (s *Store) Put(provider string, cred *Credential) error {
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()

	sf, err := s.read()
	if err != nil {
		return err
	}

	cred.Provider = provider
	sf.Credentials[provider] = cred
	return s.write(sf)
}

func (s *Store) Delete(provider string) error {
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()

	sf, err := s.read()
	if err != nil {
		return err
	}

	delete(sf.Credentials, provider)
	return s.write(sf)
}

func (s *Store) List() (map[string]*Credential, error) {
	sf, err := s.read()
	if err != nil {
		return nil, err
	}
	return sf.Credentials, nil
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
