package detect

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Detect processes that hold a memory-access handle to the game — the signature
// of an *external* map hack that reads game memory without injecting a DLL.

const (
	systemHandleInformation = 16 // SYSTEM_INFORMATION_CLASS
	statusInfoLenMismatch   = 0xC0000004

	// Process access rights that indicate memory snooping / manipulation.
	procVMRead   = 0x0010
	procVMWrite  = 0x0020
	procVMOp     = 0x0008
	procMemMask  = procVMRead | procVMWrite | procVMOp
	dupSameAcces = 0x00000002 // DUPLICATE_SAME_ACCESS
)

var (
	modntdll                     = windows.NewLazySystemDLL("ntdll.dll")
	procNtQuerySystemInformation = modntdll.NewProc("NtQuerySystemInformation")
	kGetProcessId                = modkernel32detect.NewProc("GetProcessId")
)

var modkernel32detect = windows.NewLazySystemDLL("kernel32.dll")

// systemHandleEntry mirrors SYSTEM_HANDLE_TABLE_ENTRY_INFO (class 16), 32-bit.
type systemHandleEntry struct {
	UniqueProcessID       uint16
	CreatorBackTraceIndex uint16
	ObjectTypeIndex       uint8
	HandleAttributes      uint8
	HandleValue           uint16
	Object                uintptr
	GrantedAccess         uint32
}

// ScanExternalAccess returns threats for processes (other than the game itself,
// hlauncher, and trusted system/game binaries) that hold a VM_READ/WRITE handle
// to the game process gamePID.
func (e *Engine) ScanExternalAccess(gamePID uint32) []Threat {
	if gamePID == 0 {
		return nil
	}
	entries := querySystemHandles()
	if entries == nil {
		return nil
	}

	self := uint32(windows.GetCurrentProcessId())
	// Cache: owner pid -> (name, path); and whether we've already flagged it.
	names := processNameMap()

	var out []Threat
	flagged := map[uint32]bool{}
	for i := range entries {
		h := &entries[i]
		owner := uint32(h.UniqueProcessID)
		if owner == gamePID || owner == self || owner == 0 || owner == 4 {
			continue // game itself, us, idle/system
		}
		if h.GrantedAccess&procMemMask == 0 {
			continue // not a memory-access handle
		}
		if flagged[owner] {
			continue
		}
		// Confirm this handle actually targets the game process.
		if !handleTargetsPID(owner, h.HandleValue, gamePID) {
			continue
		}
		flagged[owner] = true

		name := names[owner].name
		path := names[owner].path
		// Skip trusted owners (game/system/battle.net or validly signed).
		if e.white.trustedPath(path) || (path != "" && isTrustedSigned(path)) {
			continue
		}
		out = append(out, Threat{
			Kind: KindInjectedDLL, Severity: SevHigh, Name: name, Path: path, PID: owner,
			Reason: "게임 메모리에 접근 중인 외부 프로세스 (외부 맵핵 의심)",
		})
	}
	return dedupeSort(out)
}

// querySystemHandles returns all open handles system-wide (class 16).
func querySystemHandles() []systemHandleEntry {
	size := uint32(1 << 20)
	for attempt := 0; attempt < 8; attempt++ {
		buf := make([]byte, size)
		var retLen uint32
		r, _, _ := procNtQuerySystemInformation.Call(
			uintptr(systemHandleInformation),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(size),
			uintptr(unsafe.Pointer(&retLen)),
		)
		if uint32(r) == statusInfoLenMismatch {
			size *= 2
			continue
		}
		if r != 0 {
			return nil
		}
		// buf: ULONG NumberOfHandles; then entries.
		count := *(*uint32)(unsafe.Pointer(&buf[0]))
		entrySize := unsafe.Sizeof(systemHandleEntry{})
		base := unsafe.Pointer(&buf[0])
		// entries start after the ULONG count, aligned to pointer size.
		start := uintptr(base) + unsafe.Sizeof(uintptr(0))
		out := make([]systemHandleEntry, 0, count)
		for i := uint32(0); i < count; i++ {
			p := start + uintptr(i)*entrySize
			// bounds guard
			if p+entrySize > uintptr(base)+uintptr(len(buf)) {
				break
			}
			out = append(out, *(*systemHandleEntry)(unsafe.Pointer(p)))
		}
		return out
	}
	return nil
}

// handleTargetsPID duplicates the given handle from its owner process and checks
// whether it refers to a process whose PID == wantPID.
func handleTargetsPID(ownerPID uint32, handleValue uint16, wantPID uint32) bool {
	owner, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE, false, ownerPID)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(owner)

	var dup windows.Handle
	cur := windows.CurrentProcess()
	err = windows.DuplicateHandle(owner, windows.Handle(uintptr(handleValue)),
		cur, &dup, 0, false, dupSameAcces)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(dup)

	// GetProcessId returns nonzero only for process handles.
	pid, _, _ := kGetProcessId.Call(uintptr(dup))
	return uint32(pid) == wantPID
}

type procName struct {
	name string
	path string
}

// processNameMap maps pid -> name/path for all processes.
func processNameMap() map[uint32]procName {
	m := make(map[uint32]procName)
	for _, p := range enumProcesses() {
		m[p.PID] = procName{name: p.Name, path: p.Path}
	}
	return m
}

var _ = strings.ToLower
