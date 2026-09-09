// Command hlscan runs the detection engine once and prints threats. Diagnostic.
//
// Usage: hlscan [pid]   — if pid given, module-scan that process; otherwise scan
// all processes for signatures and module-scan any running StarCraft/Battle.net.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/detect"
	"github.com/hlauncher/hlauncher/internal/registry"
)

func main() {
	reg := registry.NewReader()
	gameDirs := []string{}
	if d := reg.GetStarCraftInstalledPath(); d != "" {
		gameDirs = append(gameDirs, d)
	}
	if d := reg.GetBattleNetPath(); d != "" {
		gameDirs = append(gameDirs, d)
	}
	fmt.Println("game dirs:", gameDirs)

	sigs := detect.DefaultSignatures()
	eng := detect.New(sigs, gameDirs)

	if len(os.Args) > 1 {
		pid64, err := strconv.ParseUint(os.Args[1], 10, 32)
		if err != nil {
			fmt.Println("bad pid:", err)
			os.Exit(2)
		}
		fmt.Printf("\n== module scan of pid %d ==\n", pid64)
		printThreats(eng.ScanGame(uint32(pid64)))
		fmt.Printf("\n== external memory-access scan of pid %d ==\n", pid64)
		printThreats(eng.ScanExternalAccess(uint32(pid64)))
		return
	}

	fmt.Println("\n== process/window signature scan ==")
	printThreats(eng.ScanProcesses())

	// Auto module-scan any running game.
	m := monitor.New(0, "Battle.net.dll", "kDetector.k")
	st := m.Snapshot()
	if st.StarCraft.Running {
		fmt.Printf("\n== module scan: StarCraft pid %d ==\n", st.StarCraft.PID)
		printThreats(eng.ScanGame(st.StarCraft.PID))
	}
	if st.BattleNet.Running {
		fmt.Printf("\n== module scan: Battle.net pid %d ==\n", st.BattleNet.PID)
		printThreats(eng.ScanGame(st.BattleNet.PID))
	}
	if !st.StarCraft.Running && !st.BattleNet.Running {
		fmt.Println("\n(게임 미실행 — 모듈 스캔 생략)")
	}
}

func printThreats(ts []detect.Threat) {
	if len(ts) == 0 {
		fmt.Println("  위협 없음")
		return
	}
	for _, t := range ts {
		fmt.Printf("  [%s] %s  %q\n      pid=%d %s\n      %s\n",
			t.Severity, t.Kind, t.Name, t.PID, t.Path, t.Reason)
	}
	fmt.Printf("  (총 %d건, HIGH %d건)\n", len(ts), countHigh(ts))
}

func countHigh(ts []detect.Threat) int {
	n := 0
	for _, t := range ts {
		if t.Severity == detect.SevHigh {
			n++
		}
	}
	return n
}

var _ = strings.ToLower
