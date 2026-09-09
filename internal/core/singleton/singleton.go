// Package singleton enforces a single running instance of hlauncher using a
// named global mutex, mirroring the original's KLauncher_SingleInstance guard.
package singleton

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Instance holds the acquired mutex handle.
type Instance struct {
	handle windows.Handle
}

// Acquire creates a named mutex. If another instance already holds it,
// Acquire returns (nil, ErrAlreadyRunning).
func Acquire(name string) (*Instance, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, namePtr)
	// CreateMutex returns a valid handle even when it already exists; the
	// distinguishing signal is ERROR_ALREADY_EXISTS from GetLastError.
	if h == 0 {
		return nil, fmt.Errorf("singleton: CreateMutex: %w", err)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return nil, ErrAlreadyRunning
	}
	return &Instance{handle: h}, nil
}

// Release frees the mutex.
func (i *Instance) Release() {
	if i != nil && i.handle != 0 {
		windows.CloseHandle(i.handle)
		i.handle = 0
	}
}

// ErrAlreadyRunning indicates another instance already holds the mutex.
var ErrAlreadyRunning = fmt.Errorf("another instance is already running")
