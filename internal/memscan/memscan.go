// Package memscan reads and searches another process's memory. It is the shared
// engine behind reading StarCraft's room/participant data.
package memscan

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modk32           = windows.NewLazySystemDLL("kernel32.dll")
	procVirtualQuery = modk32.NewProc("VirtualQueryEx")
)

type memBasicInfo struct {
	BaseAddress       uintptr
	AllocationBase    uintptr
	AllocationProtect uint32
	_                 uint32
	RegionSize        uintptr
	State             uint32
	Protect           uint32
	Type              uint32
	_                 uint32
}

const (
	memCommit    = 0x1000
	memPrivate   = 0x20000 // MEM_PRIVATE (heaps) — where JSON/strings live
	pageGuard    = 0x100
	pageNoAccess = 0x01
	pageReadable = 0x02 | 0x04 | 0x20 | 0x40

	// Skip regions larger than this: they are textures/audio/other buffers, not
	// the JSON event heaps we care about. Keeps scans cheap.
	maxScanRegion = 96 << 20
)

// Process is an opened handle for reading another process.
type Process struct {
	h windows.Handle
}

// Open opens pid for memory reading.
func Open(pid uint32) (*Process, error) {
	h, err := windows.OpenProcess(
		windows.PROCESS_VM_READ|windows.PROCESS_QUERY_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	return &Process{h: h}, nil
}

// Close releases the handle.
func (p *Process) Close() {
	if p.h != 0 {
		windows.CloseHandle(p.h)
		p.h = 0
	}
}

// Read reads size bytes at addr; returns nil on failure.
func (p *Process) Read(addr uintptr, size int) []byte {
	if size <= 0 {
		return nil
	}
	buf := make([]byte, size)
	var n uintptr
	if err := windows.ReadProcessMemory(p.h, addr, &buf[0], uintptr(size), &n); err != nil || n == 0 {
		return nil
	}
	return buf[:n]
}

func (p *Process) virtualQuery(addr uintptr) (memBasicInfo, bool) {
	var mbi memBasicInfo
	r, _, _ := procVirtualQuery.Call(uintptr(p.h), addr,
		uintptr(unsafe.Pointer(&mbi)), unsafe.Sizeof(mbi))
	return mbi, r != 0
}

// IterateRegions calls fn for each committed, readable, non-guard region.
func (p *Process) IterateRegions(fn func(base, size uintptr)) {
	var addr uintptr
	for {
		mbi, ok := p.virtualQuery(addr)
		if !ok || mbi.RegionSize == 0 {
			break
		}
		if mbi.State == memCommit && mbi.Type == memPrivate &&
			mbi.RegionSize <= maxScanRegion &&
			mbi.Protect&pageGuard == 0 &&
			mbi.Protect&pageNoAccess == 0 && mbi.Protect&pageReadable != 0 {
			fn(mbi.BaseAddress, mbi.RegionSize)
		}
		next := mbi.BaseAddress + mbi.RegionSize
		if next <= addr {
			break
		}
		addr = next
	}
}

// ScanChunks streams readable memory to fn in (base, data) chunks, so callers
// can pattern-match without copying whole regions. chunkSize bytes per call.
func (p *Process) ScanChunks(chunkSize int, fn func(base uintptr, data []byte)) {
	if chunkSize <= 0 {
		chunkSize = 1 << 20
	}
	p.IterateRegions(func(base, size uintptr) {
		for off := uintptr(0); off < size; off += uintptr(chunkSize) {
			rd := chunkSize
			if size-off < uintptr(rd) {
				rd = int(size - off)
			}
			data := p.Read(base+off, rd)
			if data != nil {
				fn(base+off, data)
			}
		}
	})
}

// Search returns absolute addresses where needle occurs.
func (p *Process) Search(needle []byte) []uintptr {
	var out []uintptr
	p.ScanChunks(1<<20, func(base uintptr, data []byte) {
		for _, idx := range IndexAll(data, needle) {
			out = append(out, base+uintptr(idx))
		}
	})
	return out
}

// IndexAll returns all offsets of needle within hay, using the optimized
// standard-library search (much faster than a naive scan on large buffers).
func IndexAll(hay, needle []byte) []int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return nil
	}
	var out []int
	off := 0
	for {
		i := bytes.Index(hay[off:], needle)
		if i < 0 {
			break
		}
		out = append(out, off+i)
		off += i + 1
	}
	return out
}
