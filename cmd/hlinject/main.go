// Command hlinject injects a DLL into a target PID using hlauncher's native
// injector and then verifies the module is loaded. For injection verification.
//
// Usage: hlinject <pid> [dllPath]
// dllPath defaults to <exeDir>\Library\kDetector.k
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hlauncher/hlauncher/internal/inject"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: hlinject <pid> [dllPath]")
		os.Exit(2)
	}
	pid64, err := strconv.ParseUint(os.Args[1], 10, 32)
	if err != nil {
		fmt.Println("invalid pid:", err)
		os.Exit(2)
	}
	pid := uint32(pid64)

	dll := ""
	if len(os.Args) >= 3 {
		dll = os.Args[2]
	} else {
		exe, _ := os.Executable()
		dll = filepath.Join(filepath.Dir(exe), "Library", "kDetector.k")
	}
	dll, _ = filepath.Abs(dll)

	fmt.Printf("target pid : %d\n", pid)
	fmt.Printf("dll        : %s\n", dll)
	if _, err := os.Stat(dll); err != nil {
		fmt.Println("dll not found:", err)
		os.Exit(1)
	}

	// Architecture guard.
	is32, err := inject.IsProcess32Bit(pid)
	if err != nil {
		fmt.Println("arch check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("target 32-bit: %v\n", is32)
	if !is32 {
		fmt.Println("ABORT: target is 64-bit; cannot inject 32-bit module")
		os.Exit(1)
	}

	fmt.Println("module loaded before:", moduleLoaded(pid, filepath.Base(dll)))

	fmt.Println("injecting (native backend) ...")
	start := time.Now()
	if err := inject.NativeInject(pid, dll); err != nil {
		fmt.Println("INJECT FAILED:", err)
		// Still check whether target survived.
		fmt.Println("target alive:", processAlive(pid))
		os.Exit(1)
	}
	fmt.Printf("inject call OK (%.0fms)\n", time.Since(start).Seconds()*1000)

	// Give the module a moment to appear, then verify.
	time.Sleep(1 * time.Second)
	loaded := moduleLoaded(pid, filepath.Base(dll))
	fmt.Println("module loaded after :", loaded)
	fmt.Println("target alive        :", processAlive(pid))

	if loaded {
		fmt.Println("\nRESULT: SUCCESS — kDetector is loaded in the target process.")
	} else {
		fmt.Println("\nRESULT: injection call returned OK but module not visible (check dependencies).")
	}
}

// moduleLoaded reports whether the target has a module whose file name matches
// nameLower (case-insensitive), scanning the 32-bit module list.
func moduleLoaded(pid uint32, name string) bool {
	nameLower := strings.ToLower(name)
	snap, err := windows.CreateToolhelp32Snapshot(
		windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)
	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
		return false
	}
	for {
		if strings.ToLower(windows.UTF16ToString(me.Module[:])) == nameLower {
			return true
		}
		if err := windows.Module32Next(snap, &me); err != nil {
			return false
		}
	}
}

func processAlive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const STILL_ACTIVE = 259
	return code == STILL_ACTIVE
}
