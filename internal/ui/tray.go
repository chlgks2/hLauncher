// Package ui implements the system-tray interface using lxn/walk, replacing the
// original launcher's browser-based web UI with a native desktop tray.
package ui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lxn/walk"

	"github.com/hlauncher/hlauncher/assets"
	"github.com/hlauncher/hlauncher/internal/app"
	"github.com/hlauncher/hlauncher/internal/config"
	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/detect"
	"github.com/hlauncher/hlauncher/internal/roster"
	"github.com/hlauncher/hlauncher/internal/version"
)

// Tray is the system-tray controller.
type Tray struct {
	app *app.App
	win *gameWindow
	mw  *walk.MainWindow
	ni  *walk.NotifyIcon

	// Menu actions we mutate on status changes.
	actStatusBnet *walk.Action
	actStatusSC   *walk.Action
	actLaunchBnet *walk.Action
	actInjectSC   *walk.Action
	actAutoInject *walk.Action

	lastStatus monitor.Status
}

// NewTray builds the hidden main window, notify icon, and context menu.
func NewTray(a *app.App) (*Tray, error) {
	win, err := buildWindow(a)
	if err != nil {
		return nil, fmt.Errorf("ui: buildWindow: %w", err)
	}
	mw := win.mw
	// Start hidden (tray only); the user opens the window from the tray menu.
	mw.SetVisible(false)

	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		return nil, fmt.Errorf("ui: NewNotifyIcon: %w", err)
	}

	t := &Tray{app: a, win: win, mw: mw, ni: ni}

	if icon := loadIcon(a); icon != nil {
		ni.SetIcon(icon)
		mw.SetIcon(icon)
	}
	ni.SetToolTip(version.AppName + " " + version.Version)

	if err := t.buildMenu(); err != nil {
		return nil, err
	}
	if err := ni.SetVisible(true); err != nil {
		return nil, fmt.Errorf("ui: SetVisible: %w", err)
	}
	// Double-clicking the tray icon opens the window.
	ni.MouseDown().Attach(func(x, y int, button walk.MouseButton) {
		if button == walk.LeftButton {
			t.win.show()
		}
	})

	// Wire app callbacks onto the UI thread via Synchronize.
	a.OnStatus(func(st monitor.Status) {
		t.mw.Synchronize(func() { t.updateStatus(st) })
	})
	a.OnLog(func(level, msg string) {
		t.mw.Synchronize(func() { t.onLog(level, msg) })
	})
	a.OnThreat(func(threats []detect.Threat) {
		t.mw.Synchronize(func() { t.onThreat(threats) })
	})
	a.OnRoster(func(parts []roster.Participant, hits []app.BlackHit, frozen bool) {
		t.mw.Synchronize(func() {
			t.onRoster(parts, hits, frozen)
			t.win.updateRoster(parts, hits, frozen)
		})
	})

	return t, nil
}

// onRoster updates the tooltip with the current room summary. Black-user alerts
// are surfaced separately via the error-level log → balloon.
func (t *Tray) onRoster(parts []roster.Participant, hits []app.BlackHit, frozen bool) {
	if len(parts) == 0 {
		return
	}
	label := "방 인원"
	if frozen {
		label = "게임 중"
	}
	tip := fmt.Sprintf("%s %s\n%s %d명", version.AppName, version.Version, label, len(parts))
	if len(hits) > 0 {
		tip += fmt.Sprintf(" · ⚠블랙 %d명", len(hits))
	}
	if len(tip) > 127 {
		tip = tip[:127]
	}
	t.ni.SetToolTip(tip)
}

// onThreat surfaces detected hacks as a prominent tray warning.
func (t *Tray) onThreat(threats []detect.Threat) {
	if len(threats) == 0 {
		return
	}
	first := threats[0]
	title := "⚠ 핵 의심 탐지"
	body := fmt.Sprintf("[%s] %s\n%s", first.Severity, first.Name, first.Reason)
	if len(threats) > 1 {
		body += fmt.Sprintf("\n외 %d건", len(threats)-1)
	}
	t.ni.ShowError(title, body)
}

func (t *Tray) buildMenu() error {
	menu := t.ni.ContextMenu()

	t.actStatusBnet = t.addInfo(menu, "Battle.net: 확인 중…")
	t.actStatusSC = t.addInfo(menu, "StarCraft: 확인 중…")
	menu.Actions().Add(walk.NewSeparatorAction())

	// Open the main window (room participants + blacklist)
	actOpen := walk.NewAction()
	actOpen.SetText("창 열기 (방 참가자 / 블랙리스트)")
	actOpen.Triggered().Attach(func() { t.win.show() })
	menu.Actions().Add(actOpen)
	menu.Actions().Add(walk.NewSeparatorAction())

	// Launch Battle.net
	t.actLaunchBnet = walk.NewAction()
	t.actLaunchBnet.SetText("Battle.net 실행")
	t.actLaunchBnet.Triggered().Attach(func() {
		if err := t.app.LaunchBattleNet(); err != nil {
			t.ni.ShowError("Battle.net 실행 실패", err.Error())
		}
	})
	menu.Actions().Add(t.actLaunchBnet)

	// Launch StarCraft (32-bit) — required for kDetector injection
	actLaunchSC := walk.NewAction()
	actLaunchSC.SetText("StarCraft(32비트) 실행")
	actLaunchSC.Triggered().Attach(func() {
		if err := t.app.LaunchStarCraft32(); err != nil {
			t.ni.ShowError("StarCraft 실행 실패", err.Error())
		} else {
			t.ni.ShowInfo("StarCraft(32비트) 실행", "32비트 StarCraft를 실행했습니다. Battle.net 로그인이 필요할 수 있습니다.")
		}
	})
	menu.Actions().Add(actLaunchSC)

	// Manual detector injection
	t.actInjectSC = walk.NewAction()
	t.actInjectSC.SetText("kDetector 수동 주입")
	t.actInjectSC.SetEnabled(false)
	t.actInjectSC.Triggered().Attach(func() {
		st := t.app.Status()
		if !st.StarCraft.Running {
			t.ni.ShowWarning("주입 불가", "StarCraft가 실행 중이 아닙니다.")
			return
		}
		if err := t.app.InjectDetector(st.StarCraft.PID); err != nil {
			t.ni.ShowError("주입 실패", err.Error())
		} else {
			t.ni.ShowInfo("주입 완료", "kDetector가 StarCraft에 주입되었습니다.")
		}
	})
	menu.Actions().Add(t.actInjectSC)

	menu.Actions().Add(walk.NewSeparatorAction())

	// Manual scan for external hacks
	actScan := walk.NewAction()
	actScan.SetText("지금 핵 검사")
	actScan.Triggered().Attach(func() {
		threats := t.app.RunDetectionNow()
		if len(threats) == 0 {
			t.ni.ShowInfo("핵 검사 완료", "의심 항목이 발견되지 않았습니다.")
		} else {
			t.onThreat(threats)
		}
	})
	menu.Actions().Add(actScan)

	menu.Actions().Add(walk.NewSeparatorAction())

	// Settings: choose Battle.net.exe
	actChoose := walk.NewAction()
	actChoose.SetText("Battle.net 경로 선택…")
	actChoose.Triggered().Attach(t.chooseBattleNet)
	menu.Actions().Add(actChoose)

	// Auto-inject toggle
	t.actAutoInject = walk.NewAction()
	t.actAutoInject.SetText("자동 주입")
	t.actAutoInject.SetCheckable(true)
	t.actAutoInject.SetChecked(t.app.Config.Get().AutoInject)
	t.actAutoInject.Triggered().Attach(func() {
		on := !t.actAutoInject.Checked()
		t.actAutoInject.SetChecked(on)
		_ = t.app.Config.Update(func(s *config.Settings) { s.AutoInject = on })
	})
	menu.Actions().Add(t.actAutoInject)

	menu.Actions().Add(walk.NewSeparatorAction())

	// About / version
	actAbout := t.addInfo(menu, fmt.Sprintf("%s v%s", version.AppName, version.Version))
	_ = actAbout

	// Exit
	actExit := walk.NewAction()
	actExit.SetText("종료")
	actExit.Triggered().Attach(func() {
		walk.App().Exit(0)
	})
	menu.Actions().Add(actExit)

	return nil
}

// addInfo adds a disabled, informational menu entry and returns it.
func (t *Tray) addInfo(menu *walk.Menu, text string) *walk.Action {
	a := walk.NewAction()
	a.SetText(text)
	a.SetEnabled(false)
	menu.Actions().Add(a)
	return a
}

func (t *Tray) chooseBattleNet() {
	dlg := &walk.FileDialog{
		Title:    "Battle.net.exe 선택",
		Filter:   "Battle.net (Battle.net.exe)|Battle.net.exe|모든 파일 (*.exe)|*.exe",
		InitialDirPath: t.app.Config.GetLastUsedPath(),
	}
	ok, err := dlg.ShowOpen(t.mw)
	if err != nil || !ok {
		return
	}
	if err := t.app.Config.SetBNetPath(dlg.FilePath); err != nil {
		t.ni.ShowError("저장 실패", err.Error())
		return
	}
	_ = t.app.Config.SetLastUsedPath(filepath.Dir(dlg.FilePath))
	t.ni.ShowInfo("경로 저장", "Battle.net 경로가 저장되었습니다.")
}

// updateStatus refreshes the menu labels/enablement from a new status.
func (t *Tray) updateStatus(st monitor.Status) {
	t.lastStatus = st

	bnet := "중지됨"
	if st.BattleNet.Running {
		bnet = fmt.Sprintf("실행 중 (pid %d)", st.BattleNet.PID)
		if st.BattleNet.Injected {
			bnet += " · 주입됨"
		}
	}
	t.actStatusBnet.SetText("Battle.net: " + bnet)

	sc := "중지됨"
	if st.StarCraft.Running {
		sc = fmt.Sprintf("실행 중 (pid %d)", st.StarCraft.PID)
		if st.StarCraft.Injected {
			sc += " · 보호됨"
		}
	}
	t.actStatusSC.SetText("StarCraft: " + sc)
	t.actInjectSC.SetEnabled(st.StarCraft.Running && !st.StarCraft.Injected)

	tip := fmt.Sprintf("%s %s\nBattle.net: %s\nStarCraft: %s",
		version.AppName, version.Version, bnet, sc)
	// NotifyIcon tooltips are limited to 127 chars.
	if len(tip) > 127 {
		tip = tip[:127]
	}
	t.ni.SetToolTip(tip)
}

func (t *Tray) onLog(level, msg string) {
	switch level {
	case "error":
		t.ni.ShowError(version.AppName, msg)
	case "warn":
		// Keep warnings quiet in the tray to avoid balloon spam; still useful on stderr.
		fmt.Fprintln(os.Stderr, "[warn]", msg)
	default:
		fmt.Fprintln(os.Stderr, "[info]", msg)
	}
}

// Run enters the message loop. It blocks until the app exits.
func (t *Tray) Run() {
	t.mw.Run()
}

// Dispose releases UI resources.
func (t *Tray) Dispose() {
	if t.ni != nil {
		t.ni.Dispose()
	}
	if t.mw != nil {
		t.mw.Dispose()
	}
}

// loadIcon writes the embedded icon to the data dir and loads it as a walk.Icon.
func loadIcon(a *app.App) *walk.Icon {
	if len(assets.Icon) == 0 {
		return nil
	}
	iconPath := filepath.Join(a.Paths.DataDir, "hlauncher.ico")
	if _, err := os.Stat(iconPath); err != nil {
		_ = os.WriteFile(iconPath, assets.Icon, 0o644)
	}
	icon, err := walk.NewIconFromFile(iconPath)
	if err != nil {
		return nil
	}
	return icon
}
