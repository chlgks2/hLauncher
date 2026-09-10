// Command hlauncher is a personal StarCraft: Remastered launcher: it reads the
// current room's participants from game memory, identifies them by battleTag,
// and manages a blacklist of bad actors. It runs as a WebView2 desktop app.
package main

import (
	"os"
	"runtime"

	"github.com/lxn/walk"

	"github.com/hlauncher/hlauncher/internal/app"
	"github.com/hlauncher/hlauncher/internal/core/singleton"
	"github.com/hlauncher/hlauncher/internal/ui"
	"github.com/hlauncher/hlauncher/internal/version"
)

func init() {
	// The WebView2 window must run on one OS thread.
	runtime.LockOSThread()
}

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

	a.Start()
	defer a.Stop()

	// Opens the window and blocks in its message loop until closed.
	if err := ui.Run(a); err != nil {
		fatal("UI 실행 실패", err)
	}
}

func fatal(title string, err error) {
	walk.MsgBox(nil, version.AppName, title+":\n"+err.Error(), walk.MsgBoxIconError)
	os.Stderr.WriteString(title + ": " + err.Error() + "\n")
	os.Exit(1)
}
