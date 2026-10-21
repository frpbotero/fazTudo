// Package settings persists user configuration (provider routing, models,
// target repository and quality policies) to a JSON file so it survives
// restarts.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/provider"
	"llm-orchestrator/internal/workspace"
)

// CurrentVersion is the file format version written by Save.
const CurrentVersion = 1

// File is the on-disk configuration. Empty fields mean "use the default".
type File struct {
	Version         int                               `json:"version"`
	DefaultProvider provider.ID                       `json:"default_provider,omitempty"`
	Failover        *bool                             `json:"failover,omitempty"`
	Providers       map[provider.ID]provider.Settings `json:"providers,omitempty"`
	Repository      string                            `json:"repository,omitempty"`
	// Quality holds the quality policy of each repository, keyed by path.
	Quality map[string]workspace.Policy `json:"quality,omitempty"`
}

// Status reports where configuration is stored and the last save outcome.
type Status struct {
	Enabled bool       `json:"enabled"`
	Path    string     `json:"path,omitempty"`
	SavedAt *time.Time `json:"saved_at,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// Store reads and writes the configuration file. A Store with an empty path
// is disabled: Load returns an empty File and Update is a no-op.
type Store struct {
	path string

	mu      sync.Mutex
	savedAt time.Time
	lastErr error
}

// NewStore creates a store for path ("" disables persistence).
func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultPath is $XDG_CONFIG_HOME/faztudo/config.json, falling back to
// ~/.config/faztudo/config.json.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "faztudo", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "faztudo", "config.json")
}

// Path returns the file location, or "" when disabled.
func (s *Store) Path() string { return s.path }

// Load reads the file. A missing file is not an error.
func (s *Store) Load() (File, error) {
	if s.path == "" {
		return File{}, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s: JSON inválido: %w", s.path, err)
	}
	if f.Version > CurrentVersion {
		return File{}, fmt.Errorf("%s: versão %d não suportada (máx. %d)", s.path, f.Version, CurrentVersion)
	}
	return f, nil
}

// Update builds the current configuration with build and writes it. Calls are
// serialised and build runs under the lock, so the last write always reflects
// the latest state even when updates race.
func (s *Store) Update(build func() File) error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f := build()
	f.Version = CurrentVersion
	err := writeAtomic(s.path, f)
	s.lastErr = err
	if err == nil {
		s.savedAt = time.Now()
	}
	return err
}

// Status returns the persistence state for the UI.
func (s *Store) Status() Status {
	if s.path == "" {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Enabled: true, Path: displayPath(s.path)}
	if !s.savedAt.IsZero() {
		t := s.savedAt
		st.SavedAt = &t
	}
	if s.lastErr != nil {
		st.Error = s.lastErr.Error()
	}
	return st
}

// writeAtomic writes JSON to a temp file in the same directory and renames it
// over the target, so a crash never leaves a truncated config.
func writeAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func displayPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
