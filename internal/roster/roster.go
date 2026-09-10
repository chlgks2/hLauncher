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
	Local     bool // true for the local player (you)
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

	// Single memory pass: read the lobby SetPlayerData slots AND capture battleTag
	// identities together. Only lobbies carry SetPlayerData (in-game/menu it's
	// gone); battleTag capture also works in the chat channel, where join events
	// flow before you enter a room. Doing both in one pass halves the per-scan
	// memory read — the main cost that made frequent scans lag the game.
	slots, tags, localSlot := scanLobby(p, store)
	inLobby := false
	for _, s := range slots {
		if s.State == "human" && s.Name != "" {
			inLobby = true
			break
		}
	}

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
		if s.SlotID == localSlot {
			s.Local = true
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

// captureInto scans memory and records battleTag identities into store.
// Returns the number of battleTag occurrences seen (used to tell "in a chat
// channel" from "in an actual match", where none exist).
func captureInto(p *memscan.Process, store *identity.Store) int {
	seen := 0
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		seen += captureChunk(data, store)
	})
	return seen
}

// captureChunk records battleTags found in one memory chunk from both sources —
// the `"battleTag":"` JSON events and the binary profile struct — and returns
// the number of JSON battleTag occurrences seen. Sharing this lets the lobby
// read capture identities in the same memory pass (no second scan).
func captureChunk(data []byte, store *identity.Store) int {
	needle := []byte(`"battleTag":"`)
	seen := 0
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
	// Second source: the binary profile struct. StarCraft keeps some room
	// members' full profile with the battleTag string exactly 0xC8 bytes before
	// the nickname. This catches players whose JSON join event has already been
	// truncated/evicted (the common coverage gap).
	captureProfileStructs(data, store)
	return seen
}

// profileNameOffset is the fixed distance from the start of the battleTag string
// to the start of the nickname in StarCraft's per-member profile struct.
const profileNameOffset = 0xC8

// captureProfileStructs records name->battleTag pairs from the binary profile
// struct: a "prefix#digits" battleTag string with the nickname at +0xC8.
func captureProfileStructs(data []byte, store *identity.Store) {
	for i := 0; i < len(data); {
		rel := bytes.IndexByte(data[i:], '#')
		if rel < 0 {
			break
		}
		h := i + rel
		// A battleTag suffix is 1-6 digits terminated by NUL.
		d := h + 1
		for d < len(data) && data[d] >= '0' && data[d] <= '9' {
			d++
		}
		if d == h+1 || d-(h+1) > 6 || d >= len(data) || data[d] != 0 {
			i = h + 1
			continue
		}
		// The tag prefix runs back from '#' over printable, non-NUL bytes.
		s := h
		for s > 0 {
			c := data[s-1]
			if c == 0 || c < 0x20 || c == 0x7f {
				break
			}
			s--
		}
		if h-s < 2 || h-s > 40 {
			i = h + 1
			continue
		}
		np := s + profileNameOffset
		if np < len(data) {
			end := np + 26
			if end > len(data) {
				end = len(data)
			}
			if name := cstr(data[np:end]); name != "" {
				store.Update(name, string(data[s:d]), "", "")
			}
		}
		i = d
	}
}

// scanLobby does ONE memory pass that both parses the lobby SetPlayerData slots
// and captures battleTag identities (JSON events + profile structs). Returns the
// slots and the JSON battleTag count. One pass ≈ half the cost of scanning for
// each separately.
func scanLobby(p *memscan.Process, store *identity.Store) (slots []Participant, tags, localSlot int) {
	spd := []byte(`"endpoint":"SetPlayerData"`)
	slp := []byte(`"endpoint":"SetLocalPlayer"`)
	localSlot = -1
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		// The local player's slot: "SetLocalPlayer","data":{"id":N,...}
		for _, idx := range memscan.IndexAll(data, slp) {
			hi := idx + 60
			if hi > len(data) {
				hi = len(data)
			}
			if v := number(data[idx:hi], "id"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					localSlot = n
				}
			}
		}
		for _, idx := range memscan.IndexAll(data, spd) {
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
			slots = append(slots, s)
		}
		tags += captureChunk(data, store)
	})
	return slots, tags, localSlot
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
	// Empty/open/closed slots use 0xffffffff as the "no storm id" sentinel; they
	// must still pass so the array walk doesn't stop at an empty slot between
	// players (that bug dropped everyone after the first gap).
	if id >= 16 || (storm >= 16 && storm != 0xffffffff) {
		return false
	}
	if data[i+0x08] > 15 { // player type (human/computer/open/closed/observer/…)
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
