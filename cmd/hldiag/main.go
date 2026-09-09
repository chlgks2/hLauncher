// Command hldiag is a console diagnostic tool: it prints resolved paths,
// registry-detected game locations, admin status, and a live process scan so
// you can verify detection works on this machine without the tray UI.
package main

import (
	"fmt"
	"time"

	"github.com/hlauncher/hlauncher/internal/config"
	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/core/paths"
	"github.com/hlauncher/hlauncher/internal/launch"
	"github.com/hlauncher/hlauncher/internal/registry"
)

func main() {
	fmt.Println("=== hlauncher diagnostics ===")

	p, err := paths.New()
	if err != nil {
		fmt.Println("paths error:", err)
		return
	}
	fmt.Println("\n[paths]")
	fmt.Println("  ExeDir     :", p.ExeDir)
	fmt.Println("  LibraryDir :", p.LibraryDir)
	fmt.Println("  DataDir    :", p.DataDir)
	fmt.Printf("  detector   : %s (exists=%v)\n", p.DetectorPath, p.CheckDetectorExists())
	fmt.Printf("  injector   : %s (exists=%v)\n", p.InjectorDLLPath, p.CheckInjectorDLLExists())
	fmt.Printf("  battlenet  : %s (exists=%v)\n", p.BattleNetDLLPath, p.CheckBattleNetDllExists())

	fmt.Println("\n[privileges]")
	fmt.Println("  IsAdmin    :", launch.IsAdmin())

	reg := registry.NewReader()
	fmt.Println("\n[registry]")
	fmt.Println("  Battle.net dir :", nonEmpty(reg.GetBattleNetPath()))
	fmt.Println("  Battle.net exe :", nonEmpty(reg.GetBattleNetExe()))
	fmt.Println("  StarCraft dir  :", nonEmpty(reg.GetStarCraftInstalledPath()))

	cfg, _ := config.NewManager(p.ConfigFile())
	fmt.Println("\n[config]")
	fmt.Printf("  %+v\n", cfg.Get())

	fmt.Println("\n[process scan] (3 ticks, 1.5s apart) — start the games to see them appear")
	m := monitor.New(1500*time.Millisecond, "Battle.net.dll", "kDetector.k")
	for i := 0; i < 3; i++ {
		st := m.Snapshot()
		fmt.Printf("  tick %d: BattleNet{run=%v pid=%d inj=%v}  StarCraft{run=%v pid=%d inj=%v}\n",
			i+1,
			st.BattleNet.Running, st.BattleNet.PID, st.BattleNet.Injected,
			st.StarCraft.Running, st.StarCraft.PID, st.StarCraft.Injected)
		if i < 2 {
			time.Sleep(1500 * time.Millisecond)
		}
	}
	fmt.Println("\ndone.")
}

func nonEmpty(s string) string {
	if s == "" {
		return "(not found)"
	}
	return s
}
