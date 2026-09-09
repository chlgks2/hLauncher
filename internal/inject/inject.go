// Package inject performs DLL injection into a target process. It offers two
// backends:
//
//   - Native: a self-contained CreateRemoteThread + LoadLibraryW injector
//     implemented in Go (the default). This is the same classic technique the
//     bundled Rust injector.dll uses (OpenProcess with 0x3A, VirtualAllocEx,
//     WriteProcessMemory, CreateRemoteThread on LoadLibraryW).
//
//   - DLL: calls the original injector.dll's exported ffi_* functions, for
//     bit-for-bit compatibility with the original launcher.
//
// Windows only (386/amd64).
package inject

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Access rights required to inject via CreateRemoteThread (== 0x3A, matching
// the original injector.dll's OpenProcess call).
const injectAccess = windows.PROCESS_CREATE_THREAD |
	windows.PROCESS_VM_OPERATION |
	windows.PROCESS_VM_READ |
	windows.PROCESS_VM_WRITE

var (
	modkernel32            = windows.NewLazySystemDLL("kernel32.dll")
	procCreateRemoteThread = modkernel32.NewProc("CreateRemoteThread")
	procVirtualAllocEx     = modkernel32.NewProc("VirtualAllocEx")
	procVirtualFreeEx      = modkernel32.NewProc("VirtualFreeEx")
	procGetExitCodeThread  = modkernel32.NewProc("GetExitCodeThread")
)

// NativeInject loads dllPath into the process identified by pid using a remote
// LoadLibraryW call. dllPath must be an absolute path that exists on disk.
func NativeInject(pid uint32, dllPath string) error {
	if pid == 0 {
		return fmt.Errorf("inject: invalid pid 0")
	}
	pathUTF16, err := windows.UTF16FromString(dllPath)
	if err != nil {
		return fmt.Errorf("inject: bad path: %w", err)
	}
	// Byte length including the terminating NUL.
	sizeBytes := uintptr(len(pathUTF16) * 2)

	hProc, err := windows.OpenProcess(injectAccess, false, pid)
	if err != nil {
		return fmt.Errorf("inject: OpenProcess(pid=%d): %w", pid, err)
	}
	defer windows.CloseHandle(hProc)

	// Allocate remote memory for the path string.
	remoteAddr, err := virtualAllocEx(hProc, sizeBytes,
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil {
		return fmt.Errorf("inject: VirtualAllocEx: %w", err)
	}
	defer virtualFreeEx(hProc, remoteAddr)

	// Write the path into the target.
	var written uintptr
	if err := windows.WriteProcessMemory(hProc, remoteAddr,
		(*byte)(unsafe.Pointer(&pathUTF16[0])), sizeBytes, &written); err != nil {
		return fmt.Errorf("inject: WriteProcessMemory: %w", err)
	}

	// Resolve LoadLibraryW in our own kernel32 (identical address in the target
	// on 32-bit Windows because kernel32 loads at the same base system-wide).
	k32 := windows.Handle(modkernel32.Handle())
	loadLib, err := windows.GetProcAddress(k32, "LoadLibraryW")
	if err != nil {
		return fmt.Errorf("inject: GetProcAddress(LoadLibraryW): %w", err)
	}

	hThread, err := createRemoteThread(hProc, loadLib, remoteAddr)
	if err != nil {
		return fmt.Errorf("inject: CreateRemoteThread: %w", err)
	}
	defer windows.CloseHandle(hThread)

	// Wait for LoadLibraryW to return.
	if _, err := windows.WaitForSingleObject(hThread, 15000); err != nil {
		return fmt.Errorf("inject: WaitForSingleObject: %w", err)
	}
	exitCode, err := getExitCodeThread(hThread)
	if err != nil {
		return fmt.Errorf("inject: GetExitCodeThread: %w", err)
	}
	// LoadLibraryW returns the module HMODULE (nonzero) on success. On 32-bit
	// the thread exit code is the low 32 bits of that handle; zero means failure.
	if exitCode == 0 {
		return fmt.Errorf("inject: remote LoadLibraryW failed (module did not load)")
	}
	return nil
}

// virtualAllocEx wraps kernel32!VirtualAllocEx (not exposed by x/sys).
func virtualAllocEx(hProc windows.Handle, size, allocType, protect uintptr) (uintptr, error) {
	r, _, e := procVirtualAllocEx.Call(
		uintptr(hProc),
		0, // lpAddress = let the system choose
		size,
		allocType,
		protect,
	)
	if r == 0 {
		if e != syscall.Errno(0) {
			return 0, e
		}
		return 0, fmt.Errorf("VirtualAllocEx returned NULL")
	}
	return r, nil
}

// virtualFreeEx releases memory allocated by virtualAllocEx (MEM_RELEASE).
func virtualFreeEx(hProc windows.Handle, addr uintptr) {
	procVirtualFreeEx.Call(uintptr(hProc), addr, 0, windows.MEM_RELEASE)
}

// getExitCodeThread wraps kernel32!GetExitCodeThread.
func getExitCodeThread(hThread windows.Handle) (uint32, error) {
	var code uint32
	r, _, e := procGetExitCodeThread.Call(
		uintptr(hThread),
		uintptr(unsafe.Pointer(&code)),
	)
	if r == 0 {
		if e != syscall.Errno(0) {
			return 0, e
		}
		return 0, fmt.Errorf("GetExitCodeThread failed")
	}
	return code, nil
}

// createRemoteThread wraps kernel32!CreateRemoteThread (not exposed by x/sys).
func createRemoteThread(hProc windows.Handle, startAddr, param uintptr) (windows.Handle, error) {
	r, _, e := procCreateRemoteThread.Call(
		uintptr(hProc),
		0,          // lpThreadAttributes
		0,          // dwStackSize
		startAddr,  // lpStartAddress (LoadLibraryW)
		param,      // lpParameter (remote path buffer)
		0,          // dwCreationFlags
		0,          // lpThreadId
	)
	if r == 0 {
		if e != syscall.Errno(0) {
			return 0, e
		}
		return 0, fmt.Errorf("CreateRemoteThread returned NULL")
	}
	return windows.Handle(r), nil
}
