// Package roster reads the current StarCraft room's participants from game
// memory. StarCraft processes the lobby via JSON events; each slot is described
// by a "SetPlayerData" event (id, name, race, color, state, latency), and each
// player's unique battleTag arrives via "OnChannelMemberJoined"/"OnMessage"
// events. We capture name->battleTag identities into a persistent store so
// players stay identifiable by battleTag even after nickname changes.
//
// Parsing is done with lightweight byte scans (no regexp) to keep the frequent
// full-heap scans cheap enough to run alongside the game.
package roster

import (
	"bytes"
	"sort"
	"strconv"

	"github.com/hlauncher/hlauncher/internal/identity"
	"github.com/hlauncher/hlauncher/internal/memscan"
)

// Participant is one player currently in the room.
type Participant struct {
	SlotID    int
	Name      string
	Race      string
	Color     string
	State     string // "human", "computer", "open", ...
	Latency   int    // ping (ms)
	BattleTag string // unique account id, e.g. "condaactivat#3668"
	ToonID    string
}

// State describes what StarCraft screen we read.
type State int

const (
	StateIdle    State = iota // a screen with no lobby/chat/game data
	StateChannel              // in the chat channel / game list (identities flow here)
	StateInGame               // in an actual match (read from the engine player array)
	StateLobby                // in a game room with participant slots
)

// Read returns the human participants of the room the StarCraft pid is in,
// captures any battleTag identities currently in memory into store, and reports
// which screen we're on so the caller can pace scans (idle == in-game).
func Read(pid uint32, store *identity.Store) ([]Participant, State, error) {
	p, err := memscan.Open(pid)
	if err != nil {
		return nil, StateIdle, err
	}
	defer p.Close()

	// Only lobbies carry full SetPlayerData; in-game/menu they're gone.
	slots := readSetPlayerData(p)
	inLobby := false
	for _, s := range slots {
		if s.State == "human" && s.Name != "" {
			inLobby = true
			break
		}
	}
	// Capture battleTags whenever channel/lobby JSON is present — that includes
	// the chat channel, where join events (name->battleTag) actually flow before
	// you enter a room. In an actual match these events are gone, so tags==0 and
	// we report StateIdle, letting the caller back off (the in-game "stop").
	tags := captureInto(p, store)

	// A chat channel carries many join events; a match has at most a stray tag
	// fragment. Require several so in-game isn't misread as the channel (which
	// would skip the in-game player-array scan).
	state := StateIdle
	switch {
	case inLobby:
		state = StateLobby
	case tags >= 4:
		state = StateChannel
	}

	byName := make(map[string]Participant)
	for _, s := range slots {
		if s.State != "human" || s.Name == "" {
			continue
		}
		if rec, ok := store.LookupName(s.Name); ok {
			s.BattleTag = rec.BattleTag
			s.ToonID = rec.Toon
		}
		byName[lower(s.Name)] = s
	}

	out := make([]Participant, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SlotID < out[j].SlotID })
	return out, state, nil
}

// InGamePlayers reads the in-game engine player array for pid and resolves
// battleTags from store (diagnostic / test entrypoint).
func InGamePlayers(pid uint32, store *identity.Store) ([]Participant, error) {
	p, err := memscan.Open(pid)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	parts := readInGamePlayers(p)
	for i := range parts {
		if rec, ok := store.LookupName(parts[i].Name); ok {
			parts[i].BattleTag = rec.BattleTag
			parts[i].ToonID = rec.Toon
		}
	}
	return parts, nil
}

// CaptureIdentities opens pid, harvests name->battleTag identities into store,
// and closes. Lighter than a full Read (no SetPlayerData pass).
func CaptureIdentities(pid uint32, store *identity.Store) error {
	p, err := memscan.Open(pid)
	if err != nil {
		return err
	}
	defer p.Close()
	captureInto(p, store)
	return nil
}

// captureInto scans for `"battleTag":"` and records every name field near it.
// Returns the number of battleTag occurrences seen (used to tell "in a chat
// channel" from "in an actual match", where none exist).
func captureInto(p *memscan.Process, store *identity.Store) int {
	needle := []byte(`"battleTag":"`)
	seen := 0
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		for _, idx := range memscan.IndexAll(data, needle) {
			seen++
			lo := idx - 500
			if lo < 0 {
				lo = 0
			}
			hi := idx + 300
			if hi > len(data) {
				hi = len(data)
			}
			win := data[lo:hi]
			battleTag := field(win, "battleTag")
			if battleTag == "" {
				continue
			}
			pretty := field(win, "prettyBattleTag")
			toon := number(win, "legacyChatToonId")
			for _, key := range []string{"name", "legacyToonName", "rawName"} {
				if nm := field(win, key); nm != "" {
					store.Update(nm, battleTag, pretty, toon)
				}
			}
		}
	})
	return seen
}

func readSetPlayerData(p *memscan.Process) []Participant {
	needle := []byte(`"endpoint":"SetPlayerData"`)
	var out []Participant
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		for _, idx := range memscan.IndexAll(data, needle) {
			hi := idx + 300
			if hi > len(data) {
				hi = len(data)
			}
			win := data[idx:hi]
			s := Participant{SlotID: -1}
			if v := number(win, "id"); v != "" {
				s.SlotID, _ = strconv.Atoi(v)
			}
			s.Name = field(win, "name")
			s.Race = field(win, "race")
			s.Color = field(win, "color")
			s.State = field(win, "state")
			if v := number(win, "latency"); v != "" {
				s.Latency, _ = strconv.Atoi(v)
			}
			out = append(out, s)
		}
	})
	return out
}

// field extracts a JSON string value: "<key>":"<value>".
func field(win []byte, key string) string {
	marker := make([]byte, 0, len(key)+4)
	marker = append(marker, '"')
	marker = append(marker, key...)
	marker = append(marker, '"', ':', '"')
	i := bytes.Index(win, marker)
	if i < 0 {
		return ""
	}
	start := i + len(marker)
	end := bytes.IndexByte(win[start:], '"')
	if end < 0 || end > 80 {
		return ""
	}
	return string(win[start : start+end])
}

// number extracts a JSON numeric value: "<key>":<digits>.
func number(win []byte, key string) string {
	marker := make([]byte, 0, len(key)+3)
	marker = append(marker, '"')
	marker = append(marker, key...)
	marker = append(marker, '"', ':')
	i := bytes.Index(win, marker)
	if i < 0 {
		return ""
	}
	start := i + len(marker)
	end := start
	for end < len(win) && win[end] >= '0' && win[end] <= '9' && end-start < 12 {
		end++
	}
	if end == start {
		return ""
	}
	return string(win[start:end])
}

// readInGamePlayers finds StarCraft's in-game player array and returns the named
// players. Once a match starts the lobby JSON is gone, but the engine keeps the
// classic BroodWar player table: 36-byte structs laid out back-to-back —
//
//	+0x00 u32 playerID   +0x04 u32 stormId
//	+0x08 u8 type (2=human)   +0x09 u8 race   +0x0A u8 team
//	+0x0B char name[25] (NUL-padded)
//
// We locate it by signature (a run of well-formed structs), so it survives ASLR
// with no injection. Returns the run with the most named players.
func readInGamePlayers(p *memscan.Process) []Participant {
	const stride = 36
	var best []Participant
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		n := len(data) - stride
		for i := 0; i <= n; i++ {
			// Fast gate: playerID and stormId are small u32s, so bytes 1-3 and 5-7
			// are zero. This rejects almost all offsets with a few cheap compares
			// before the fuller structural check.
			if data[i] >= 16 || data[i+1] != 0 || data[i+2] != 0 || data[i+3] != 0 ||
				data[i+5] != 0 || data[i+6] != 0 || data[i+7] != 0 {
				continue
			}
			// Name must start with a printable byte — this rejects the vast
			// zero-filled regions that otherwise pass the small-integer gate.
			if nb := data[i+0x0B]; nb < 0x20 || nb == 0x7f {
				continue
			}
			if !playerStructAt(data, i) {
				continue
			}
			// Walk consecutive struct-shaped slots from here.
			var run []Participant
			named := 0
			for j := i; j+stride <= len(data) && playerStructAt(data, j); j += stride {
				name := cstr(data[j+0x0B : j+0x0B+25])
				typ := data[j+0x08]
				if name != "" && typ == 2 { // human
					named++
					run = append(run, Participant{
						SlotID:  int(u32(data[j:])),
						Name:    name,
						Race:    raceName(data[j+0x09]),
						State:   "human",
						Latency: -1, // ping not available from this array
					})
				}
			}
			if named > len(best) {
				best = run
			}
			i += stride * len(run) // skip past this run
		}
	})
	return best
}

// playerStructAt reports whether data[i:] plausibly begins a BroodWar player
// struct: a small playerID/stormId and a valid type, with a printable-or-empty
// name. Kept strict enough that random memory rarely forms a run of these.
func playerStructAt(data []byte, i int) bool {
	if i+36 > len(data) {
		return false
	}
	id := u32(data[i:])
	storm := u32(data[i+4:])
	if id >= 16 || storm >= 16 {
		return false
	}
	switch data[i+0x08] { // type: inactive/computer/human/rescue/open/neutral/closed
	case 0, 1, 2, 3, 4, 5, 6, 7, 8:
	default:
		return false
	}
	if data[i+0x09] > 6 { // race 0..6 (zerg/terran/protoss/… /select/random)
		return false
	}
	// Name must be printable up to a NUL, then NUL padding.
	name := data[i+0x0B : i+0x0B+25]
	nul := false
	for _, c := range name {
		if c == 0 {
			nul = true
			continue
		}
		if nul { // non-NUL after a NUL: not a clean padded name
			return false
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func u32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func raceName(r byte) string {
	switch r {
	case 0:
		return "Zerg"
	case 1:
		return "Terran"
	case 2:
		return "Protoss"
	case 6:
		return "Random"
	default:
		return ""
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
