// Package blacklist stores flagged ("black") users keyed by their unique
// battleTag, with a reason and the time they were added. Persisted as JSON so
// it survives restarts (and can later be synced across PCs).
package blacklist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry is one blacklisted user.
type Entry struct {
	BattleTag string    `json:"battleTag"`      // unique key, e.g. "condaactivat#3668"
	Name      string    `json:"name,omitempty"` // last seen nickname
	Reason    string    `json:"reason"`         // why they were blacklisted
	AddedAt   time.Time `json:"addedAt"`        // when
}

// List is a persistent, concurrency-safe blacklist keyed by battleTag.
type List struct {
	mu      sync.RWMutex
	path    string
	entries map[string]Entry // key: lowercased battleTag
}

// Load reads the blacklist from path (missing file = empty list).
func Load(path string) (*List, error) {
	l := &List{path: path, entries: make(map[string]Entry)}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	if len(b) == 0 {
		return l, nil
	}
	var arr []Entry
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, err
	}
	for _, e := range arr {
		if e.BattleTag == "" {
			continue
		}
		l.entries[key(e.BattleTag)] = e
	}
	return l, nil
}

// Save writes the blacklist to disk atomically.
func (l *List) Save() error {
	l.mu.RLock()
	arr := make([]Entry, 0, len(l.entries))
	for _, e := range l.entries {
		arr = append(arr, e)
	}
	path := l.path
	l.mu.RUnlock()

	sort.Slice(arr, func(i, j int) bool { return arr[i].AddedAt.After(arr[j].AddedAt) })
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

// Add inserts or updates a blacklist entry and saves. If the user already
// exists, the reason/name are updated but the original AddedAt is kept.
func (l *List) Add(battleTag, name, reason string) error {
	if strings.TrimSpace(battleTag) == "" {
		return nil
	}
	l.mu.Lock()
	k := key(battleTag)
	e, ok := l.entries[k]
	if !ok {
		e = Entry{BattleTag: battleTag, AddedAt: time.Now()}
	}
	if name != "" {
		e.Name = name
	}
	e.Reason = reason
	l.entries[k] = e
	l.mu.Unlock()
	return l.Save()
}

// Remove deletes an entry by battleTag and saves.
func (l *List) Remove(battleTag string) error {
	l.mu.Lock()
	delete(l.entries, key(battleTag))
	l.mu.Unlock()
	return l.Save()
}

// Get looks up an entry by battleTag.
func (l *List) Get(battleTag string) (Entry, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.entries[key(battleTag)]
	return e, ok
}

// IsBlack reports whether a battleTag is blacklisted.
func (l *List) IsBlack(battleTag string) bool {
	_, ok := l.Get(battleTag)
	return ok
}

// Match resolves a participant to a blacklist entry: it prefers the unique
// battleTag, and falls back to the lobby display name (for players whose
// battleTag couldn't be resolved). The identifier used for an entry is stored
// in its BattleTag field (a real battleTag when known, else the name).
func (l *List) Match(battleTag, name string) (Entry, bool) {
	if battleTag != "" {
		if e, ok := l.Get(battleTag); ok {
			return e, true
		}
	}
	if name != "" {
		if e, ok := l.Get(name); ok {
			return e, true
		}
	}
	return Entry{}, false
}

// All returns all entries, newest first.
func (l *List) All() []Entry {
	l.mu.RLock()
	arr := make([]Entry, 0, len(l.entries))
	for _, e := range l.entries {
		arr = append(arr, e)
	}
	l.mu.RUnlock()
	sort.Slice(arr, func(i, j int) bool { return arr[i].AddedAt.After(arr[j].AddedAt) })
	return arr
}

// Count returns the number of entries.
func (l *List) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

func key(battleTag string) string { return strings.ToLower(strings.TrimSpace(battleTag)) }
