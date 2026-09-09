package inject

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// ErrArchMismatch is returned when the target process is 64-bit and therefore
// cannot load our 32-bit modules (or be driven by our 32-bit injector).
var ErrArchMismatch = fmt.Errorf("target process is 64-bit; cannot inject a 32-bit module")

// IsProcess32Bit reports whether the process pid is a 32-bit process, which is
// a precondition for injecting our 32-bit assets from 32-bit hlauncher.
//
// On a 64-bit OS, IsWow64Process is true exactly for 32-bit (WOW64) processes.
// On a 32-bit OS every process is 32-bit and IsWow64Process is false — but a
// 32-bit hlauncher on a 32-bit OS only ever sees 32-bit targets, so we treat
// "our own bitness" as the reference and detect that case too.
func IsProcess32Bit(pid uint32) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false, fmt.Errorf("arch: OpenProcess(pid=%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)

	var targetWow64 bool
	if err := windows.IsWow64Process(h, &targetWow64); err != nil {
		return false, fmt.Errorf("arch: IsWow64Process: %w", err)
	}
	if targetWow64 {
		// Definitely a 32-bit process running under WOW64 on a 64-bit OS.
		return true, nil
	}

	// Target is "native". Determine whether the OS itself is 32-bit by checking
	// our own process: if hlauncher (32-bit) is NOT under WOW64, the OS is 32-bit
	// and the native target is therefore also 32-bit.
	cur, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false,
		uint32(windows.GetCurrentProcessId()))
	if err != nil {
		// Fall back to assuming 64-bit OS (the common case) → native target is 64-bit.
		return false, nil
	}
	defer windows.CloseHandle(cur)
	var selfWow64 bool
	if err := windows.IsWow64Process(cur, &selfWow64); err != nil {
		return false, nil
	}
	// selfWow64 == false here means the OS is 32-bit → native target is 32-bit.
	return !selfWow64, nil
}
