// Package recents keeps a persistent list of players you have recently shared a
// room or game with — a "recently played with" history, distinct from the full
// identity capture (which also records people you only saw in the chat channel).
package recents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// newSession is how long apart two encounters must be to count as separate
// games (so a single match doesn't inflate the count on every scan).
const newSession = 10 * time.Minute

// maxKeep caps how many recent players we retain.
const maxKeep = 300

// Entry is one recently-encountered player.
type Entry struct {
	BattleTag string    `json:"battleTag,omitempty"`
	Name      string    `json:"name"`
	LastSeen  time.Time `json:"lastSeen"`
	Count     int       `json:"count"`
}

// Store is the persistent recent-players list, keyed by battleTag (or name).
type Store struct {
	mu    sync.RWMutex
	path  string
	byKey map[string]Entry
	dirty bool
}

func key(name, battleTag string) string {
	if battleTag != "" {
		return battleTag
	}
	return "name:" + strings.ToLower(name)
}

// Load reads the store from disk (missing file = empty).
func Load(path string) (*Store, error) {
	s := &Store{path: path, byKey: make(map[string]Entry)}
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
	var arr []Entry
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, err
	}
	for _, e := range arr {
		if e.Name == "" && e.BattleTag == "" {
			continue
		}
		s.byKey[key(e.Name, e.BattleTag)] = e
	}
	return s, nil
}

// Record notes that a player was just encountered in a room/game.
func (s *Store) Record(name, battleTag string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(name, battleTag)
	now := time.Now()
	e, ok := s.byKey[k]
	if !ok {
		s.byKey[k] = Entry{BattleTag: battleTag, Name: name, LastSeen: now, Count: 1}
		s.dirty = true
		return
	}
	if name != "" {
		e.Name = name // keep the latest nickname
	}
	if battleTag != "" {
		e.BattleTag = battleTag
	}
	if now.Sub(e.LastSeen) > newSession {
		e.Count++
	}
	e.LastSeen = now
	s.byKey[k] = e
	s.dirty = true
}

// All returns the recent players, most-recent first, capped at maxKeep.
func (s *Store) All() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.byKey))
	for _, e := range s.byKey {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > maxKeep {
		out = out[:maxKeep]
	}
	return out
}

// Remove deletes an entry by its key (battleTag, or "name:<lower>").
func (s *Store) Remove(k string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[k]; ok {
		delete(s.byKey, k)
		s.dirty = true
	}
}

// Count returns the number of recent players.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byKey)
}

// Save persists the store if it changed, keeping only the most recent maxKeep.
func (s *Store) Save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	arr := make([]Entry, 0, len(s.byKey))
	for _, e := range s.byKey {
		arr = append(arr, e)
	}
	s.dirty = false
	path := s.path
	s.mu.Unlock()

	sort.Slice(arr, func(i, j int) bool { return arr[i].LastSeen.After(arr[j].LastSeen) })
	if len(arr) > maxKeep {
		arr = arr[:maxKeep]
	}
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
