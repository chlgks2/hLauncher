// Bindings to the original bundled injector.dll (Rust) for bit-for-bit
// compatibility with the original launcher's injection path.
//
// Exported ABI recovered from the binary (cdecl unless noted):
//
//	ffi_check_is_elevated()                              -> bool          // no args
//	ffi_execute_injection(pid: u32, dll_path_utf8: *u8)  -> u32/bool      // OpenProcess(0x3A)+LoadLibraryW
//	ffi_check_process_and_module(proc_utf8, mod_utf8)    -> ProcessCheckResult
//	ffi_check_renderer_processes(name_utf8, len_or_flag) -> RendererResult
//	ffi_verify_signature(ptr)                            -> bool          // stdcall (ret 4), 32-byte magic compare
package inject

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// InjectorDLL wraps a loaded injector.dll and its exported functions.
type InjectorDLL struct {
	dll                  *windows.DLL
	procExecuteInjection *windows.Proc
	procCheckElevated    *windows.Proc
	procCheckProcMod     *windows.Proc
}

var (
	loadedInjector *InjectorDLL
	injectorOnce   sync.Once
	injectorErr    error
)

// LoadInjectorDLL loads injector.dll from the given absolute path once and
// caches it. Subsequent calls return the cached instance.
func LoadInjectorDLL(path string) (*InjectorDLL, error) {
	injectorOnce.Do(func() {
		dll, err := windows.LoadDLL(path)
		if err != nil {
			injectorErr = fmt.Errorf("LoadDLL(%s): %w", path, err)
			return
		}
		inj := &InjectorDLL{dll: dll}
		// FindProc returns an error only if the symbol is missing; tolerate
		// missing optional symbols so one absent export doesn't break the rest.
		inj.procExecuteInjection, _ = dll.FindProc("ffi_execute_injection")
		inj.procCheckElevated, _ = dll.FindProc("ffi_check_is_elevated")
		inj.procCheckProcMod, _ = dll.FindProc("ffi_check_process_and_module")
		loadedInjector = inj
	})
	return loadedInjector, injectorErr
}

// Inject calls injector.dll!ffi_execute_injection(pid, utf8Path).
func (i *InjectorDLL) Inject(pid uint32, dllPath string) error {
	if i == nil || i.procExecuteInjection == nil {
		return fmt.Errorf("injector.dll: ffi_execute_injection not available")
	}
	// The DLL expects a UTF-8, NUL-terminated path pointer.
	pathBytes, err := utf8CString(dllPath)
	if err != nil {
		return err
	}
	r, _, _ := i.procExecuteInjection.Call(
		uintptr(pid),
		uintptr(unsafe.Pointer(&pathBytes[0])),
	)
	// Keep pathBytes alive across the call.
	runtime.KeepAlive(pathBytes)
	// The Rust FFI returns nonzero/true on success.
	if r == 0 {
		return fmt.Errorf("injector.dll: injection reported failure (pid=%d)", pid)
	}
	return nil
}

// IsElevated calls injector.dll!ffi_check_is_elevated().
func (i *InjectorDLL) IsElevated() bool {
	if i == nil || i.procCheckElevated == nil {
		return false
	}
	r, _, _ := i.procCheckElevated.Call()
	return r != 0
}

// utf8CString returns a NUL-terminated UTF-8 byte slice for s.
func utf8CString(s string) ([]byte, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return nil, fmt.Errorf("string contains NUL")
		}
	}
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b, nil
}

