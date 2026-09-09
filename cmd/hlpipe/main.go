// Command hlpipe runs ONLY the KLauncherPipe server (console, verbose) so we
// can verify the kDetector handshake in isolation: start it, inject kDetector
// into 32-bit StarCraft, and watch whether the game survives.
//
// Usage: hlpipe [launcherPath] [version] [userToken] [userNickName]
package main

import (
	"log"
	"os"
	"time"

	"github.com/hlauncher/hlauncher/internal/core/paths"
	"github.com/hlauncher/hlauncher/internal/core/pipe"
)

func main() {
	p, err := paths.New()
	if err != nil {
		log.Fatal(err)
	}
	_ = p.EnsureDirectoriesExist()

	info := pipe.HandshakeInfo{
		LauncherPath: arg(1, ""),
		AppDataPath:  p.DataDir,
		Version:      arg(2, "4.1.1"),
		UserToken:    arg(3, ""),
		UserNickName: arg(4, ""),
	}
	log.Printf("handshake info: launcherPath=%q appDataPath=%q version=%q token=%q nick=%q",
		info.LauncherPath, info.AppDataPath, info.Version, info.UserToken, info.UserNickName)

	kp := pipe.NewKLauncherPipe(info, func(f string, a ...interface{}) { log.Printf(f, a...) })
	kp.OnMessage(func(m pipe.Message) {
		log.Printf("RECV from kDetector: command=%q data=%v", m.Command, m.Data)
	})
	if err := kp.Listen(); err != nil {
		log.Fatalf("listen failed: %v", err)
	}
	log.Println("KLauncherPipe server running. Inject kDetector now. Ctrl+C to stop.")
	for {
		time.Sleep(time.Hour)
	}
}

func arg(i int, def string) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return def
}
