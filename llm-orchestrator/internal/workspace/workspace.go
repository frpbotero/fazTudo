// Package workspace manages the target repository and caches its stack
// discovery, on disk when a cache directory is set.
package workspace

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"llm-orchestrator/internal/quality"
)

// ErrNotConfigured is returned when no repository has been set.
var ErrNotConfigured = errors.New("nenhum repositório configurado")

// Report is the outcome of a repository discovery.
type Report struct {
	Path      string    `json:"path"`
	Stacks    []Stack   `json:"stacks"`
	Summary   string    `json:"summary"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
	Simulated bool      `json:"simulated"`
	CreatedAt time.Time `json:"created_at"`
	// Graph is the absolute path of the code graph (graphify), "" without one.
	Graph string `json:"graph,omitempty"`
}

// State is the public view of the workspace.
type State struct {
	Path      string  `json:"path"`
	Discovery *Report `json:"discovery,omitempty"`
	Policy    Policy  `json:"policy"`
	// Suggested are the check commands detected from the manifests.
	Suggested []quality.Command `json:"suggested"`
}

// Manager holds the configured repository, its cached discovery and the
// quality policy of every repository configured so far.
type Manager struct {
	mu        sync.RWMutex
	path      string
	report    *Report
	suggested []quality.Command
	policies  map[string]Policy
	// cacheDir keeps each repository's discovery across restarts; "" keeps
	// it in memory only.
	cacheDir string
}

// NewManager creates a manager, optionally pre-configured with a repository.
func NewManager(path string) (*Manager, error) {
	m := &Manager{policies: map[string]Policy{}}
	if path != "" {
		if _, err := m.SetPath(path); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Path returns the configured repository, or "" when unset.
func (m *Manager) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// SetPath validates and sets the repository. Changing it drops the cached
// discovery; setting the same path keeps it.
func (m *Manager) SetPath(p string) (string, error) {
	resolved, err := Resolve(p)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if resolved != m.path {
		m.path = resolved
		m.report = m.loadCachedLocked()
	}
	m.suggested = quality.Suggest(resolved)
	return resolved, nil
}

// Policy returns the quality policy of the current repository.
func (m *Manager) Policy() Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policyLocked()
}

func (m *Manager) policyLocked() Policy {
	p, ok := m.policies[m.path]
	if !ok {
		return DefaultPolicy()
	}
	p.Checks = slices.Clone(p.Checks)
	p.Protected = slices.Clone(p.Protected)
	return p
}

// SetPolicy validates and stores the policy of the current repository.
func (m *Manager) SetPolicy(p Policy) (Policy, error) {
	p = p.Normalize()
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.path == "" {
		return Policy{}, ErrNotConfigured
	}
	m.policies[m.path] = p
	m.suggested = quality.Suggest(m.path)
	return p, nil
}

// Checks returns the commands to run for the current repository.
func (m *Manager) Checks() []quality.Command {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p := m.policyLocked(); !p.AutoChecks {
		return p.Checks
	}
	return slices.Clone(m.suggested)
}

// Policies returns every stored policy, keyed by repository path.
func (m *Manager) Policies() map[string]Policy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]Policy, len(m.policies))
	for k, v := range m.policies {
		out[k] = v
	}
	return out
}

// LoadPolicies replaces the stored policies, skipping invalid entries.
func (m *Manager) LoadPolicies(policies map[string]Policy) []error {
	var errs []error
	m.mu.Lock()
	defer m.mu.Unlock()
	for path, p := range policies {
		p = p.Normalize()
		if err := p.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("política de %s: %w", path, err))
			continue
		}
		m.policies[path] = p
	}
	return errs
}

// Discovery returns the cached report for the current repository, if any.
func (m *Manager) Discovery() *Report {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.report == nil {
		return nil
	}
	r := *m.report
	r.Stacks = slices.Clone(m.report.Stacks)
	return &r
}

// Store caches a report, unless the repository changed meanwhile.
func (m *Manager) Store(r Report) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Path == m.path {
		m.report = &r
		m.suggested = quality.Suggest(m.path) // discovery may have run installs
		m.saveCachedLocked(r)
	}
}

// Invalidate drops the cached discovery so the next run repeats it.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.report = nil
	if file := m.cacheFileLocked(); file != "" {
		os.Remove(file)
	}
}

// SetCacheDir persists discoveries under dir, so a restart reuses the last
// one of the repository instead of mapping it again.
func (m *Manager) SetCacheDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheDir = dir
	if m.report == nil {
		m.report = m.loadCachedLocked()
	}
}

func (m *Manager) cacheFileLocked() string {
	if m.cacheDir == "" || m.path == "" {
		return ""
	}
	sum := sha1.Sum([]byte(m.path))
	return filepath.Join(m.cacheDir, hex.EncodeToString(sum[:])+".json")
}

// loadCachedLocked reads the saved discovery of the current repository; a
// missing or unreadable file means there is none.
func (m *Manager) loadCachedLocked() *Report {
	file := m.cacheFileLocked()
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var r Report
	if json.Unmarshal(data, &r) != nil || r.Path != m.path {
		return nil
	}
	return &r
}

// saveCachedLocked is best effort: a failed write only costs a rediscovery.
func (m *Manager) saveCachedLocked(r Report) {
	file := m.cacheFileLocked()
	if file == "" {
		return
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil || os.MkdirAll(filepath.Dir(file), 0o755) != nil {
		return
	}
	os.WriteFile(file, data, 0o644)
}

// State returns a snapshot for the UI.
func (m *Manager) State() State {
	st := State{Path: m.Path(), Discovery: m.Discovery(), Policy: m.Policy()}
	m.mu.RLock()
	st.Suggested = slices.Clone(m.suggested)
	m.mu.RUnlock()
	if st.Suggested == nil {
		st.Suggested = []quality.Command{}
	}
	return st
}

// Resolve expands "~", makes the path absolute and checks it is a directory.
func Resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("informe o caminho do repositório")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("repositório inacessível: %s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("o caminho não é um diretório: %s", abs)
	}
	return abs, nil
}
