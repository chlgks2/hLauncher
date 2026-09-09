// Package launch starts external processes (the games) and provides
// admin-privilege helpers, mirroring the original launcher's utils package.
package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process starts exePath with args in workDir (workDir defaults to the exe's
// directory). It returns the new process PID.
func Process(exePath string, args []string, workDir string) (uint32, error) {
	if _, err := os.Stat(exePath); err != nil {
		return 0, fmt.Errorf("launch: executable not found: %w", err)
	}
	if workDir == "" {
		workDir = filepath.Dir(exePath)
	}
	attr := &os.ProcAttr{
		Dir:   workDir,
		Files: []*os.File{nil, nil, nil},
	}
	argv := append([]string{exePath}, args...)
	proc, err := os.StartProcess(exePath, argv, attr)
	if err != nil {
		return 0, fmt.Errorf("launch: StartProcess: %w", err)
	}
	pid := uint32(proc.Pid)
	// Detach; we monitor via the toolhelp snapshot, not this handle.
	proc.Release()
	return pid, nil
}

// --- admin helpers ---

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modshell32.NewProc("ShellExecuteExW")
)

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	swShowNormal          = 1
)

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIconOrMonitor windows.Handle
	hProcess     windows.Handle
}

// AsAdmin re-launches exePath elevated via ShellExecuteEx "runas". This shows
// the UAC prompt. Returns after the elevated process is created.
func AsAdmin(exePath string, args []string, workDir string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	var paramPtr *uint16
	if len(args) > 0 {
		paramPtr, _ = windows.UTF16PtrFromString(joinArgs(args))
	}
	if workDir == "" {
		workDir = filepath.Dir(exePath)
	}
	dir, _ := windows.UTF16PtrFromString(workDir)

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: paramPtr,
		lpDirectory:  dir,
		nShow:        swShowNormal,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if e != syscall.Errno(0) {
			return fmt.Errorf("launch: ShellExecuteEx(runas): %w", e)
		}
		return fmt.Errorf("launch: ShellExecuteEx(runas) failed")
	}
	if info.hProcess != 0 {
		windows.CloseHandle(info.hProcess)
	}
	return nil
}

// IsAdmin reports whether the current process is running elevated.
func IsAdmin() bool {
	var sid *windows.SID
	// S-1-5-32-544 = BUILTIN\Administrators
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	token := windows.Token(0) // pseudo-handle for the current process token
	member, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

// CurrentExecutablePath returns the absolute path of the running executable.
func CurrentExecutablePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	return p, nil
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		// naive quoting for paths with spaces
		if containsSpace(a) {
			out += "\"" + a + "\""
		} else {
			out += a
		}
	}
	return out
}

func containsSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return true
		}
	}
	return false
}
