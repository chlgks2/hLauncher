// Command hlblack manages the blacklist from the console.
//
//	hlblack list
//	hlblack add <battleTag|name> <reason...>
//	hlblack remove <battleTag|name>
//	hlblack room                       show current room participants + black status
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hlauncher/hlauncher/internal/blacklist"
	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/core/paths"
	"github.com/hlauncher/hlauncher/internal/identity"
	"github.com/hlauncher/hlauncher/internal/roster"
)

func main() {
	p, err := paths.New()
	if err != nil {
		fmt.Println("paths:", err)
		os.Exit(1)
	}
	_ = p.EnsureDirectoriesExist()
	blPath := filepath.Join(p.DataDir, "blacklist.json")
	bl, err := blacklist.Load(blPath)
	if err != nil {
		fmt.Println("블랙리스트 로드 실패:", err)
		os.Exit(1)
	}

	cmd := "list"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "list":
		list(bl)
	case "add":
		if len(os.Args) < 3 {
			fmt.Println("usage: hlblack add <battleTag|name> <reason...>")
			os.Exit(2)
		}
		id := os.Args[2]
		reason := strings.Join(os.Args[3:], " ")
		if err := bl.Add(id, id, reason); err != nil {
			fmt.Println("추가 실패:", err)
			os.Exit(1)
		}
		fmt.Printf("블랙 등록: %s (사유: %s)\n", id, reason)
	case "remove":
		if len(os.Args) < 3 {
			fmt.Println("usage: hlblack remove <battleTag|name>")
			os.Exit(2)
		}
		if err := bl.Remove(os.Args[2]); err != nil {
			fmt.Println("삭제 실패:", err)
			os.Exit(1)
		}
		fmt.Println("삭제:", os.Args[2])
	case "room":
		room(bl)
	default:
		fmt.Println("commands: list | add <id> <reason> | remove <id> | room")
	}
}

func p2Dir() string {
	p, err := paths.New()
	if err != nil {
		return "."
	}
	_ = p.EnsureDirectoriesExist()
	return p.DataDir
}

func list(bl *blacklist.List) {
	entries := bl.All()
	fmt.Printf("=== 블랙리스트: %d명 ===\n", len(entries))
	for _, e := range entries {
		fmt.Printf("  %-24s  name=%-16s  사유=%q  등록=%s\n",
			e.BattleTag, e.Name, e.Reason, e.AddedAt.Format("2006-01-02 15:04"))
	}
}

func room(bl *blacklist.List) {
	m := monitor.New(0, "Battle.net.dll", "kDetector.k")
	st := m.Snapshot()
	if !st.StarCraft.Running {
		fmt.Println("StarCraft 미실행")
		return
	}
	store, _ := identity.Load(filepath.Join(p2Dir(), "identities.json"))
	parts, _, err := roster.Read(st.StarCraft.PID, store)
	_ = store.Save()
	if err != nil {
		fmt.Println("roster read 실패:", err)
		return
	}
	fmt.Printf("=== 현재 방 참가자: %d명 ===\n", len(parts))
	for _, p := range parts {
		status := ""
		if e, ok := bl.Match(p.BattleTag, p.Name); ok {
			status = fmt.Sprintf("  ⚠ 블랙(%s)", e.Reason)
		}
		id := p.BattleTag
		if id == "" {
			id = "(태그없음)"
		}
		fmt.Printf("  슬롯%d  %-18s  핑%-4dms  %s%s\n", p.SlotID, p.Name, p.Latency, id, status)
	}
}
