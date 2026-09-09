// Command hlsnoop simulates an EXTERNAL map hack for testing: it opens the
// target process with PROCESS_VM_READ and keeps reading its memory, exactly as
// an out-of-process memory-snooping cheat would. It does NOT inject anything.
//
// Usage: hlsnoop <pid>
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: hlsnoop <pid>")
		os.Exit(2)
	}
	pid64, _ := strconv.ParseUint(os.Args[1], 10, 32)
	h, err := windows.OpenProcess(windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, uint32(pid64))
	if err != nil {
		fmt.Println("OpenProcess failed:", err)
		os.Exit(1)
	}
	defer windows.CloseHandle(h)
	fmt.Printf("holding PROCESS_VM_READ handle to pid %d; reading memory... (Ctrl+C to stop)\n", pid64)

	buf := make([]byte, 64)
	for {
		var n uintptr
		// Read from a low address; error is fine — we just keep the handle busy.
		windows.ReadProcessMemory(h, 0x400000, &buf[0], uintptr(len(buf)), &n)
		time.Sleep(500 * time.Millisecond)
	}
}
