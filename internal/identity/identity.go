// Package identity maintains a persistent map of player name -> battleTag,
// accumulated from StarCraft's channel-join events over time. Because those
// events are only briefly in memory, we capture them continuously and remember
// them forever, so a player can be tracked by unique battleTag even if they
// change or recreate their nickname.
package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// validName rejects names that were mis-captured from fragmented JSON in memory:
// anything with control bytes, a NUL, or the Unicode replacement char is a torn
// string, not a real nickname.
func validName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return false
		}
	}
	return true
}

// Record is a known identity.
type Record struct {
	Name      string    `json:"name"`
	BattleTag string    `json:"battleTag"`
	Pretty    string    `json:"pretty,omitempty"`
	Toon      string    `json:"toon,omitempty"`
	LastSeen  time.Time `json:"lastSeen"`
}

// Store is a persistent identity map keyed by lowercased name (and by toon id).
type Store struct {
	mu     sync.RWMutex
	path   string
	byName map[string]Record
	byToon map[string]Record
	dirty  bool
}

// Load reads the identity store from disk (missing file = empty).
func Load(path string) (*Store, error) {
	s := &Store{
		path:   path,
		byName: make(map[string]Record),
		byToon: make(map[string]Record),
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	var arr []Record
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, err
	}
	for _, r := range arr {
		// Drop entries that were mis-captured from fragmented JSON in older runs.
		if !validName(r.Name) || !validName(r.BattleTag) || !strings.Contains(r.BattleTag, "#") {
			s.dirty = true // rewrite a cleaned store on next Save
			continue
		}
		if !validName(r.Pretty) {
			r.Pretty = ""
			s.dirty = true
		}
		s.byName[strings.ToLower(r.Name)] = r
		if r.Toon != "" {
			s.byToon[r.Toon] = r
		}
	}
	return s, nil
}

// Update records or refreshes an identity. Returns true if it was new/changed.
func (s *Store) Update(name, battleTag, pretty, toon string) bool {
	if !validName(name) || !validName(battleTag) || !strings.Contains(battleTag, "#") {
		return false
	}
	if !validName(pretty) {
		pretty = "" // torn display name; keep the entry but drop the garbage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.ToLower(name)
	prev, existed := s.byName[k]
	if existed && prev.BattleTag == battleTag {
		// already known; just bump LastSeen occasionally
		prev.LastSeen = time.Now()
		s.byName[k] = prev
		if toon != "" {
			s.byToon[toon] = prev
		}
		return false
	}
	r := Record{Name: name, BattleTag: battleTag, Pretty: pretty, Toon: toon, LastSeen: time.Now()}
	s.byName[k] = r
	if toon != "" {
		s.byToon[toon] = r
	}
	s.dirty = true
	return true
}

// LookupName resolves a display/account name to a known identity.
func (s *Store) LookupName(name string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byName[strings.ToLower(name)]
	return r, ok
}

// LookupToon resolves by legacyChatToonId.
func (s *Store) LookupToon(toon string) (Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.byToon[toon]
	return r, ok
}

// Count returns the number of known identities.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byName)
}

// Save persists the store to disk if it changed since the last save.
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	arr := make([]Record, 0, len(s.byName))
	for _, r := range s.byName {
		arr = append(arr, r)
	}
	s.dirty = false
	path := s.path
	s.mu.Unlock()

	data, err := json.MarshalIndent(arr, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
