// Command hlmod lists loaded modules of a target PID (32-bit module list),
// optionally filtered by a name substring. Diagnostic only.
//
// Usage: hlmod <pid> [nameSubstr]
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: hlmod <pid> [nameSubstr]")
		os.Exit(2)
	}
	pid64, err := strconv.ParseUint(os.Args[1], 10, 32)
	if err != nil {
		fmt.Println("invalid pid:", err)
		os.Exit(2)
	}
	filter := ""
	if len(os.Args) >= 3 {
		filter = strings.ToLower(os.Args[2])
	}
	snap, err := windows.CreateToolhelp32Snapshot(
		windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, uint32(pid64))
	if err != nil {
		fmt.Printf("snapshot failed (pid=%d): %v\n", pid64, err)
		os.Exit(1)
	}
	defer windows.CloseHandle(snap)

	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
		fmt.Printf("Module32First failed: %v\n", err)
		os.Exit(1)
	}
	n, shown := 0, 0
	for {
		name := windows.UTF16ToString(me.Module[:])
		path := windows.UTF16ToString(me.ExePath[:])
		n++
		if filter == "" || strings.Contains(strings.ToLower(name), filter) {
			fmt.Printf("  %-30s %s\n", name, path)
			shown++
		}
		if err := windows.Module32Next(snap, &me); err != nil {
			break
		}
	}
	fmt.Printf("total modules: %d, shown: %d\n", n, shown)
}
