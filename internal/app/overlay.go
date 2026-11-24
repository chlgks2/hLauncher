package app

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/hlauncher/hlauncher/internal/overlay"
	"github.com/hlauncher/hlauncher/internal/roster"
)

// ---------------------------------------------------------------------------
// In-game overlay
//
// The roster is only useful while you are looking at it, and during a match you
// are looking at the game, not at this window. The overlay puts the same list —
// names, race, ping, and whether somebody is blacklisted — on top of StarCraft
// so it can be read without leaving the game.
//
// It draws an ordinary desktop window over the game; nothing is injected and
// the game is never written to, so this stays within the read-only line the
// rest of this build holds to.
// ---------------------------------------------------------------------------

// overlayPrefs is the small amount of state worth remembering between runs.
type overlayPrefs struct {
	Enabled bool `json:"enabled"`
}

func (a *App) overlayPrefsPath() string {
	return filepath.Join(a.Paths.DataDir, "overlay.json")
}

// initOverlay creates the overlay window and restores the user's last choice.
// A failure here is never fatal: the launcher works fine without it.
func (a *App) initOverlay() {
	o := overlay.New()
	if err := o.Start(); err != nil {
		a.logf("warn", "오버레이를 열지 못했습니다: %v", err)
		return
	}
	a.Overlay = o

	// Default on: the overlay is the reason most people want this during a game.
	prefs := overlayPrefs{Enabled: true}
	if b, err := os.ReadFile(a.overlayPrefsPath()); err == nil {
		_ = json.Unmarshal(b, &prefs)
	}
	o.SetEnabled(prefs.Enabled)
}

// OverlayEnabled reports whether the overlay is currently shown.
func (a *App) OverlayEnabled() bool {
	if a.Overlay == nil {
		return false
	}
	return a.Overlay.Enabled()
}

// SetOverlayEnabled turns the overlay on or off and remembers the choice.
func (a *App) SetOverlayEnabled(on bool) {
	if a.Overlay == nil {
		return
	}
	a.Overlay.SetEnabled(on)
	b, err := json.MarshalIndent(overlayPrefs{Enabled: on}, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(a.Paths.DataDir, 0o755)
	_ = os.WriteFile(a.overlayPrefsPath(), b, 0o644)
	if on {
		a.logf("info", "인게임 오버레이 켜짐")
	} else {
		a.logf("info", "인게임 오버레이 꺼짐")
	}
}

// pushOverlay mirrors the published roster onto the overlay.
func (a *App) pushOverlay(parts []roster.Participant, hits []BlackHit) {
	if a.Overlay == nil {
		return
	}
	a.Overlay.SetTarget(a.Status().StarCraft.PID)

	reason := make(map[string]string, len(hits))
	for _, h := range hits {
		reason[h.Participant.Name] = h.Entry.Reason
	}
	rows := make([]overlay.Row, 0, len(parts))
	for _, p := range parts {
		_, white := a.White.Match(p.BattleTag, p.Name)
		rows = append(rows, overlay.Row{
			Name:  p.Name,
			Race:  p.Race,
			Ping:  p.Latency,
			Black: reason[p.Name],
			White: white,
			Me:    p.Local,
		})
	}
	a.Overlay.Update(rows)
}
