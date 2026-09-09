// Package config persists user settings (game paths, preferences) as JSON
// under the per-user data directory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings is the JSON-serialized user configuration.
type Settings struct {
	// BNetPath is the full path to Battle.net.exe chosen/detected for launch.
	BNetPath string `json:"bnetPath"`
	// StarcraftPath is the full path to StarCraft.exe (optional; usually launched via Battle.net).
	StarcraftPath string `json:"starcraftPath"`
	// LastUsedPath is the last directory used in a file-open dialog.
	LastUsedPath string `json:"lastUsedPath"`
	// AutoInject enables automatic injection when the games are detected.
	AutoInject bool `json:"autoInject"`
	// LaunchArgs are extra arguments appended when launching StarCraft.
	LaunchArgs string `json:"launchArgs"`
}

// Manager loads and saves Settings with a mutex for concurrent access.
type Manager struct {
	mu   sync.RWMutex
	path string
	data Settings
}

// NewManager creates a Manager backed by the given config file path and loads it.
func NewManager(configPath string) (*Manager, error) {
	m := &Manager{
		path: configPath,
		// Auto-injection of kDetector is disabled by default: the original
		// server is gone, so kDetector cannot validate and would just close the
		// game. hlauncher now focuses on its own detection engine.
		data: Settings{AutoInject: false},
	}
	if err := m.Load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Load reads the config file. A missing file is not an error (defaults are kept).
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	b, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var s Settings
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	m.data = s
	return nil
}

// Save writes the current settings to disk atomically (temp file + rename).
func (m *Manager) Save() error {
	m.mu.RLock()
	b, err := json.MarshalIndent(m.data, "", "  ")
	path := m.path
	m.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get returns a copy of the current settings.
func (m *Manager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.data
}

// Update applies fn to the settings under lock and saves.
func (m *Manager) Update(fn func(s *Settings)) error {
	m.mu.Lock()
	fn(&m.data)
	m.mu.Unlock()
	return m.Save()
}

func (m *Manager) GetBNetPath() string     { return m.Get().BNetPath }
func (m *Manager) GetLastUsedPath() string { return m.Get().LastUsedPath }

func (m *Manager) SetBNetPath(p string) error {
	return m.Update(func(s *Settings) { s.BNetPath = p })
}

func (m *Manager) SetLastUsedPath(p string) error {
	return m.Update(func(s *Settings) { s.LastUsedPath = p })
}
