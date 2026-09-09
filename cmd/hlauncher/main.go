// Command hlauncher is an anti-cheat launcher for StarCraft: it monitors for
// the Battle.net and StarCraft processes and injects the bundled detector
// module (kDetector.k) into the game. It runs as a system-tray application.
package main

import (
	"os"

	"github.com/lxn/walk"

	"github.com/hlauncher/hlauncher/internal/app"
	"github.com/hlauncher/hlauncher/internal/core/singleton"
	"github.com/hlauncher/hlauncher/internal/ui"
	"github.com/hlauncher/hlauncher/internal/version"
)

func main() {
	// Enforce a single running instance.
	inst, err := singleton.Acquire(`Global\` + version.AppName + `_SingleInstance`)
	if err == singleton.ErrAlreadyRunning {
		walk.MsgBox(nil, version.AppName,
			version.AppName+"가 이미 실행 중입니다.", walk.MsgBoxIconInformation)
		return
	} else if err != nil {
		fatal("단일 인스턴스 확인 실패", err)
		return
	}
	defer inst.Release()

	a, err := app.New()
	if err != nil {
		fatal("초기화 실패", err)
		return
	}

	tray, err := ui.NewTray(a)
	if err != nil {
		fatal("트레이 생성 실패", err)
		return
	}
	defer tray.Dispose()

	a.Start()
	defer a.Stop()

	// Enter the Windows message loop; blocks until the user exits.
	tray.Run()
}

func fatal(title string, err error) {
	walk.MsgBox(nil, version.AppName, title+":\n"+err.Error(), walk.MsgBoxIconError)
	os.Stderr.WriteString(title + ": " + err.Error() + "\n")
	os.Exit(1)
}
