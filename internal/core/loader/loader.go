// Package loader verifies and loads the bundled native DLLs. Verification uses
// WinVerifyTrust to confirm each asset carries a valid Authenticode signature
// before it is injected, mirroring the original launcher's DllLoader.
package loader

import (
	"fmt"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DllLoader verifies signatures and loads DLLs by absolute path.
type DllLoader struct {
	mu       sync.Mutex
	loaded   map[string]*windows.DLL
	verified map[string]bool
	// RequireSignature, when true, refuses to load unsigned/untrusted assets.
	RequireSignature bool
}

// New returns a DllLoader. If requireSignature is true, LoadDLL fails for any
// asset that does not pass WinVerifyTrust.
func New(requireSignature bool) *DllLoader {
	return &DllLoader{
		loaded:           make(map[string]*windows.DLL),
		verified:         make(map[string]bool),
		RequireSignature: requireSignature,
	}
}

// IsVerified reports whether path has a valid Authenticode signature (cached).
func (l *DllLoader) IsVerified(path string) bool {
	l.mu.Lock()
	if v, ok := l.verified[path]; ok {
		l.mu.Unlock()
		return v
	}
	l.mu.Unlock()

	ok := VerifyAuthenticode(path) == nil
	l.mu.Lock()
	l.verified[path] = ok
	l.mu.Unlock()
	return ok
}

// IsLoaded reports whether path is currently loaded in this process.
func (l *DllLoader) IsLoaded(path string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.loaded[path]
	return ok
}

// LoadDLL verifies (if required) and loads a DLL into this process. Note that
// injection into *other* processes does not go through here — this is only for
// DLLs hlauncher itself must load (e.g. injector.dll).
func (l *DllLoader) LoadDLL(path string) (*windows.DLL, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("loader: asset missing: %w", err)
	}
	if l.RequireSignature && !l.IsVerified(path) {
		return nil, fmt.Errorf("loader: signature verification failed for %s", path)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if d, ok := l.loaded[path]; ok {
		return d, nil
	}
	d, err := windows.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("loader: LoadDLL(%s): %w", path, err)
	}
	l.loaded[path] = d
	return d, nil
}

// --- WinVerifyTrust ---

var (
	modwintrust          = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust   = modwintrust.NewProc("WinVerifyTrust")
)

// GUID for the generic Authenticode policy (WINTRUST_ACTION_GENERIC_VERIFY_V2).
var wintrustActionGenericVerifyV2 = windows.GUID{
	Data1: 0xaac56b,
	Data2: 0xcd44,
	Data3: 0x11d0,
	Data4: [8]byte{0x8c, 0xc2, 0x00, 0xc0, 0x4f, 0xc2, 0x95, 0xee},
}

const (
	wtdUINone           = 2
	wtdRevokeNone       = 0
	wtdChoiceFile       = 1
	wtdStateActionVerify   = 1
	wtdStateActionClose    = 2
	wtdCacheOnlyURLRetrieval = 0x00001000
)

type wintrustFileInfo struct {
	CbStruct       uint32
	PcwszFilePath  *uint16
	HFile          windows.Handle
	PgKnownSubject *windows.GUID
}

type wintrustData struct {
	CbStruct            uint32
	PPolicyCallbackData uintptr
	PSIPClientData      uintptr
	DwUIChoice          uint32
	FdwRevocationChecks uint32
	DwUnionChoice       uint32
	FileOrCatalogOrBlob uintptr // pointer to wintrustFileInfo (union)
	DwStateAction       uint32
	HWVTStateData       windows.Handle
	PwszURLReference    *uint16
	DwProvFlags         uint32
	DwUIContext         uint32
	PSignatureSettings  uintptr
}

// VerifyAuthenticode returns nil if path carries a valid, trusted Authenticode
// signature (embedded). A non-nil error describes why verification failed.
func VerifyAuthenticode(path string) error {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	fileInfo := wintrustFileInfo{
		PcwszFilePath: pathPtr,
	}
	fileInfo.CbStruct = uint32(unsafe.Sizeof(fileInfo))

	data := wintrustData{
		DwUIChoice:          wtdUINone,
		FdwRevocationChecks: wtdRevokeNone,
		DwUnionChoice:       wtdChoiceFile,
		FileOrCatalogOrBlob: uintptr(unsafe.Pointer(&fileInfo)),
		DwStateAction:       wtdStateActionVerify,
		DwProvFlags:         wtdCacheOnlyURLRetrieval,
	}
	data.CbStruct = uint32(unsafe.Sizeof(data))

	guid := wintrustActionGenericVerifyV2
	r, _, _ := procWinVerifyTrust.Call(
		0, // hwnd = INVALID_HANDLE_VALUE for none (0 acceptable with WTD_UI_NONE)
		uintptr(unsafe.Pointer(&guid)),
		uintptr(unsafe.Pointer(&data)),
	)

	// Always release the state data.
	data.DwStateAction = wtdStateActionClose
	procWinVerifyTrust.Call(
		0,
		uintptr(unsafe.Pointer(&guid)),
		uintptr(unsafe.Pointer(&data)),
	)

	if r != 0 {
		return fmt.Errorf("WinVerifyTrust: 0x%X", uint32(r))
	}
	return nil
}
