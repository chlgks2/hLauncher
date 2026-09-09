// Package monitor watches for the Battle.net and StarCraft processes, reports
// whether the anti-cheat modules are already injected, and fires callbacks when
// status changes. It mirrors the original ProcessMonitor.
package monitor

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Process names we care about (compared case-insensitively).
const (
	BattleNetExe = "Battle.net.exe"
	StarCraftExe = "StarCraft.exe"
)

// ProcessInfo describes a single detected process.
type ProcessInfo struct {
	PID      uint32
	Name     string
	Running  bool
	Injected bool // true if the expected module is loaded in this process
}

// Status is a snapshot of both games.
type Status struct {
	BattleNet ProcessInfo
	StarCraft ProcessInfo
}

// StatusHandler is invoked (from the monitor goroutine) whenever status changes.
type StatusHandler func(Status)

// Monitor periodically scans processes and reports status changes.
type Monitor struct {
	interval time.Duration

	// Module names whose presence marks a process as "injected".
	bnetModule string // Battle.net.dll
	scModule   string // kDetector.k

	mu       sync.Mutex
	handlers []StatusHandler
	last     Status
	hasLast  bool

	stop chan struct{}
	done chan struct{}
}

// New creates a Monitor. bnetModule/scModule are the lowercase module file
// names that indicate injection (e.g. "battle.net.dll", "kdetector.k").
func New(interval time.Duration, bnetModule, scModule string) *Monitor {
	if interval <= 0 {
		interval = 1500 * time.Millisecond
	}
	return &Monitor{
		interval:   interval,
		bnetModule: strings.ToLower(bnetModule),
		scModule:   strings.ToLower(scModule),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// AddStatusHandler registers a callback for status changes.
func (m *Monitor) AddStatusHandler(h StatusHandler) {
	m.mu.Lock()
	m.handlers = append(m.handlers, h)
	m.mu.Unlock()
}

// Start begins the monitoring loop in a background goroutine.
func (m *Monitor) Start() {
	go m.loop()
}

// Stop halts the monitoring loop and waits for it to exit.
func (m *Monitor) Stop() {
	select {
	case <-m.stop:
		// already stopped
	default:
		close(m.stop)
	}
	<-m.done
}

// Snapshot returns the current status immediately (synchronous scan).
func (m *Monitor) Snapshot() Status {
	return m.scan()
}

func (m *Monitor) loop() {
	defer close(m.done)
	t := time.NewTicker(m.interval)
	defer t.Stop()

	// Fire once immediately.
	m.tick()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *Monitor) tick() {
	cur := m.scan()
	m.mu.Lock()
	changed := !m.hasLast || cur != m.last
	m.last = cur
	m.hasLast = true
	handlers := append([]StatusHandler(nil), m.handlers...)
	m.mu.Unlock()

	if changed {
		for _, h := range handlers {
			h(cur)
		}
	}
}

func (m *Monitor) scan() Status {
	procs := enumProcesses()
	var st Status
	st.BattleNet.Name = BattleNetExe
	st.StarCraft.Name = StarCraftExe

	for _, p := range procs {
		switch strings.ToLower(p.name) {
		case strings.ToLower(BattleNetExe):
			st.BattleNet.PID = p.pid
			st.BattleNet.Running = true
			if m.bnetModule != "" {
				st.BattleNet.Injected = processHasModule(p.pid, m.bnetModule)
			}
		case strings.ToLower(StarCraftExe):
			st.StarCraft.PID = p.pid
			st.StarCraft.Running = true
			if m.scModule != "" {
				st.StarCraft.Injected = processHasModule(p.pid, m.scModule)
			}
		}
	}
	return st
}

type procEntry struct {
	pid  uint32
	name string
}

// enumProcesses lists running processes via the toolhelp snapshot API.
func enumProcesses() []procEntry {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil
	}
	var out []procEntry
	for {
		name := windows.UTF16ToString(entry.ExeFile[:])
		out = append(out, procEntry{pid: entry.ProcessID, name: name})
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}
	return out
}

// processHasModule reports whether the process pid has a loaded module whose
// file name equals moduleLower (already lowercased).
func processHasModule(pid uint32, moduleLower string) bool {
	// TH32CS_SNAPMODULE snapshots the 32-bit module list of the target.
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
		name := windows.UTF16ToString(me.Module[:])
		if strings.ToLower(name) == moduleLower {
			return true
		}
		if err := windows.Module32Next(snap, &me); err != nil {
			break
		}
	}
	return false
}
