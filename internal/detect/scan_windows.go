package detect

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Module is a loaded module in a process.
type Module struct {
	Name string
	Path string
}

// Process is a running process.
type Process struct {
	PID  uint32
	Name string
	Path string
}

// enumModules lists the 32-bit modules loaded in pid.
func enumModules(pid uint32) []Module {
	snap, err := windows.CreateToolhelp32Snapshot(
		windows.TH32CS_SNAPMODULE|windows.TH32CS_SNAPMODULE32, pid)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var me windows.ModuleEntry32
	me.Size = uint32(unsafe.Sizeof(me))
	if err := windows.Module32First(snap, &me); err != nil {
		return nil
	}
	var out []Module
	for {
		out = append(out, Module{
			Name: windows.UTF16ToString(me.Module[:]),
			Path: windows.UTF16ToString(me.ExePath[:]),
		})
		if err := windows.Module32Next(snap, &me); err != nil {
			break
		}
	}
	return out
}

// enumProcesses lists all running processes with their image paths.
func enumProcesses() []Process {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return nil
	}
	var out []Process
	for {
		p := Process{PID: pe.ProcessID, Name: windows.UTF16ToString(pe.ExeFile[:])}
		p.Path = imagePath(pe.ProcessID)
		out = append(out, p)
		if err := windows.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return out
}

// imagePath returns the full image path of pid, or "" if inaccessible.
func imagePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// --- window enumeration ---

var (
	moduser32              = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows        = moduser32.NewProc("EnumWindows")
	procGetWindowTextW     = moduser32.NewProc("GetWindowTextW")
	procGetWindowThreadPID = moduser32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible    = moduser32.NewProc("IsWindowVisible")
)

// enumWindowTitles returns visible top-level window titles grouped by owner pid.
func enumWindowTitles() map[uint32][]string {
	result := make(map[uint32][]string)
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
			return 1 // continue
		}
		buf := make([]uint16, 256)
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		title := windows.UTF16ToString(buf[:n])
		var pid uint32
		procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && title != "" {
			result[pid] = append(result[pid], title)
		}
		return 1 // continue enumeration
	})
	procEnumWindows.Call(cb, 0)
	return result
}
