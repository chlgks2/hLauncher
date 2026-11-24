// Command hloverlay shows the in-game overlay on its own, with sample rows, so
// its placement and appearance can be checked without running a real match.
//
//	hloverlay <starcraft pid> [seconds]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/hlauncher/hlauncher/internal/overlay"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: hloverlay <pid> [seconds]")
		return
	}
	pid64, err := strconv.ParseUint(os.Args[1], 10, 32)
	if err != nil {
		fmt.Println("pid 오류:", err)
		return
	}
	secs := 20
	if len(os.Args) >= 3 {
		secs, _ = strconv.Atoi(os.Args[2])
	}

	o := overlay.New()
	if err := o.Start(); err != nil {
		fmt.Println("오버레이 생성 실패:", err)
		return
	}
	o.SetTarget(uint32(pid64))
	o.Update([]overlay.Row{
		{Name: "Player1", Race: "Terran", Ping: 38, Me: true},
		{Name: "Player2", Race: "Zerg", Ping: 51},
		{Name: "Player3", Race: "Protoss", Ping: 107, White: true},
		{Name: "Player4", Race: "Terran", Ping: 246, Black: "사유 예시"},
		{Name: "Player5", Race: "Random", Ping: -1},
	})
	o.SetEnabled(true)

	fmt.Printf("오버레이 표시 중 (pid %d 창을 따라감, %d초)\n", pid64, secs)
	time.Sleep(time.Duration(secs) * time.Second)
	o.Close()
	fmt.Println("종료")
}
