// Package ui renders the launcher's window with an embedded WebView2 (the modern
// HTML/CSS interface), driven by the Go backend. It replaces the original
// browser-based web UI and the earlier native-widget tray.
package ui

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	webview "github.com/jchv/go-webview2"

	"github.com/hlauncher/hlauncher/internal/app"
	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/detect"
	"github.com/hlauncher/hlauncher/internal/roster"
	"github.com/hlauncher/hlauncher/internal/version"
)

//go:embed web/index.html
var indexHTML string

type webUI struct {
	app   *app.App
	w     webview.WebView
	ready bool
}

// JSON shapes sent to the page.
type wsPlayer struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	Ping      int    `json:"ping"`
	Race      string `json:"race"`
	BattleTag string `json:"battleTag"`
	Black     string `json:"black"`
	White     bool   `json:"white"`
	Me        bool   `json:"me"`
}

type wsRecent struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	BattleTag string `json:"battleTag"`
	LastSeen  string `json:"lastSeen"`
	Count     int    `json:"count"`
}

type wsSummary struct {
	State     string `json:"state"`
	Count     int    `json:"count"`
	BlackHits int    `json:"blackHits"`
	Tags      int    `json:"tags"`
}

type wsBlack struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Added  string `json:"added"`
}

// Run creates the WebView2 window, wires the app callbacks to it, and blocks in
// the window's message loop until the window is closed.
func Run(a *app.App) error {
	u := &webUI{app: a}
	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  "H런처 " + version.Version,
			Width:  980,
			Height: 660,
		},
	})
	if w == nil {
		return fmt.Errorf("WebView2 창 생성 실패 (WebView2 런타임이 필요합니다)")
	}
	u.w = w
	defer w.Destroy()

	// JS -> Go bridge.
	w.Bind("hlReady", func() {
		u.ready = true
		u.pushStatus()
		u.pushBlacklist()
		u.pushWhitelist()
		u.pushRecents()
		u.refreshRoster()
	})
	w.Bind("hlRefresh", func() { u.app.RefreshNow(); u.refreshRoster() })
	w.Bind("hlRecents", func() { u.pushRecents() })
	w.Bind("hlAddBlack", func(name, tag, reason string) {
		_ = a.AddBlack(tag, name, reason)
		u.pushBlacklist()
		u.refreshRoster()
	})
	w.Bind("hlRemoveBlack", func(id string) {
		_ = a.RemoveBlack(id)
		u.pushBlacklist()
		u.refreshRoster()
	})
	w.Bind("hlWhitelist", func() { u.pushWhitelist() })
	w.Bind("hlAddWhite", func(name, tag, memo string) {
		_ = a.AddWhite(tag, name, memo)
		u.pushWhitelist()
		u.refreshRoster()
	})
	w.Bind("hlRemoveWhite", func(id string) {
		_ = a.RemoveWhite(id)
		u.pushWhitelist()
		u.refreshRoster()
	})
	w.Bind("hlRemoveRecent", func(key string) {
		a.RemoveRecent(key)
		u.pushRecents()
	})

	w.SetHtml(indexHTML)

	// Go -> JS: app callbacks push data. They arrive on background goroutines, so
	// hop onto the UI thread with Dispatch before touching the webview.
	a.OnRoster(func(parts []roster.Participant, hits []app.BlackHit, frozen bool) {
		u.w.Dispatch(func() { u.sendRoster(parts, hits, frozen) })
	})
	a.OnStatus(func(monitor.Status) {
		u.w.Dispatch(func() { u.pushStatus() })
	})
	a.OnThreat(func(th []detect.Threat) {
		if len(th) == 0 {
			return
		}
		msg := "⚠ 핵 의심: " + th[0].Name
		u.w.Dispatch(func() { u.eval("hl.toast(" + jsStr(msg) + ",'danger')") })
	})
	a.OnLog(func(level, msg string) {
		if level == "error" {
			u.w.Dispatch(func() { u.eval("hl.toast(" + jsStr(msg) + ",'danger')") })
		}
	})

	w.Run()
	return nil
}

func jsStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func (u *webUI) eval(js string) {
	if u.ready {
		u.w.Eval(js)
	}
}

func (u *webUI) refreshRoster() {
	parts, hits, frozen := u.app.Displayed()
	u.sendRoster(parts, hits, frozen)
}

func (u *webUI) sendRoster(parts []roster.Participant, hits []app.BlackHit, frozen bool) {
	reason := make(map[string]string, len(hits))
	for _, h := range hits {
		reason[h.Participant.Name] = h.Entry.Reason
	}
	players := make([]wsPlayer, 0, len(parts))
	for _, p := range parts {
		_, white := u.app.White.Match(p.BattleTag, p.Name)
		players = append(players, wsPlayer{
			Slot: p.SlotID, Name: p.Name, Ping: p.Latency, Race: p.Race,
			BattleTag: p.BattleTag, Black: reason[p.Name], White: white, Me: p.Local,
		})
	}
	pj, _ := json.Marshal(players)

	state := "idle"
	switch {
	case frozen:
		state = "ingame"
	case len(parts) > 0:
		state = "lobby"
	}
	sj, _ := json.Marshal(wsSummary{
		State: state, Count: len(parts), BlackHits: len(hits), Tags: u.app.Identity.Count(),
	})
	u.eval("hl.setRoster(" + string(pj) + ")")
	u.eval("hl.setSummary(" + string(sj) + ")")
}

func (u *webUI) pushBlacklist() {
	entries := u.app.Black.All()
	rows := make([]wsBlack, 0, len(entries))
	for _, e := range entries {
		id := e.BattleTag
		if id == "" {
			id = e.Name
		}
		rows = append(rows, wsBlack{
			ID: id, Name: e.Name, Reason: e.Reason,
			Added: e.AddedAt.Format("2006-01-02"),
		})
	}
	pj, _ := json.Marshal(rows)
	u.eval("hl.setBlack(" + string(pj) + ")")
}

func (u *webUI) pushWhitelist() {
	entries := u.app.White.All()
	rows := make([]wsBlack, 0, len(entries))
	for _, e := range entries {
		id := e.BattleTag
		if id == "" {
			id = e.Name
		}
		rows = append(rows, wsBlack{
			ID: id, Name: e.Name, Reason: e.Reason,
			Added: e.AddedAt.Format("2006-01-02"),
		})
	}
	pj, _ := json.Marshal(rows)
	u.eval("hl.setWhite(" + string(pj) + ")")
}

func relTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분 전", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d시간 전", int(d.Hours()))
	default:
		return fmt.Sprintf("%d일 전", int(d.Hours()/24))
	}
}

func (u *webUI) pushRecents() {
	entries := u.app.Recents.All()
	rows := make([]wsRecent, 0, len(entries))
	for _, e := range entries {
		key := e.BattleTag
		if key == "" {
			key = "name:" + strings.ToLower(e.Name)
		}
		rows = append(rows, wsRecent{
			Key: key, Name: e.Name, BattleTag: e.BattleTag,
			LastSeen: relTime(e.LastSeen), Count: e.Count,
		})
	}
	pj, _ := json.Marshal(rows)
	u.eval("hl.setRecents(" + string(pj) + ")")
}

func (u *webUI) pushStatus() {
	st := u.app.Status()
	sj, _ := json.Marshal(map[string]interface{}{
		"scRunning":   st.StarCraft.Running,
		"bnetRunning": st.BattleNet.Running,
		"version":     version.Version,
	})
	u.eval("hl.setStatus(" + string(sj) + ")")
}
