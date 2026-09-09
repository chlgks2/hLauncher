// Package detect is hlauncher's own anti-cheat detection engine (stage 1:
// external hack detection). It scans the game and system for injected DLLs,
// known hack processes, and suspicious windows — without injecting anything.
package detect

import (
	"sort"
	"strings"
	"sync"
)

// Severity ranks a detection.
type Severity int

const (
	SevLow Severity = iota
	SevMedium
	SevHigh
)

func (s Severity) String() string {
	switch s {
	case SevHigh:
		return "HIGH"
	case SevMedium:
		return "MEDIUM"
	default:
		return "LOW"
	}
}

// Kind classifies a threat.
type Kind string

const (
	KindInjectedDLL     Kind = "injected_dll"     // unsigned/out-of-place module in the game
	KindKnownHackModule Kind = "known_hack_module" // module matched a signature
	KindKnownHackProc   Kind = "known_hack_proc"   // process matched a signature
	KindSuspiciousProc  Kind = "suspicious_proc"   // unsigned process with a hack-like window
)

// Threat is a single detection result.
type Threat struct {
	Kind     Kind
	Severity Severity
	Name     string // module or process name
	Path     string // full path if known
	PID      uint32 // owning process (game pid for modules)
	Reason   string // human-readable explanation
}

// Engine performs detection scans.
type Engine struct {
	mu    sync.RWMutex
	sigs  Signatures
	white *whitelist
}

// New builds an engine with the given signatures and path whitelist context.
func New(sigs Signatures, gameDirs []string) *Engine {
	return &Engine{
		sigs:  sigs.normalized(),
		white: newWhitelist(gameDirs),
	}
}

// SetSignatures replaces the signature set (e.g. after loading an updated DB).
func (e *Engine) SetSignatures(sigs Signatures) {
	e.mu.Lock()
	e.sigs = sigs.normalized()
	e.mu.Unlock()
}

// ScanGame inspects the loaded modules of a game process (pid) and returns
// threats: injected/out-of-place unsigned DLLs and signature matches.
func (e *Engine) ScanGame(pid uint32) []Threat {
	e.mu.RLock()
	sigs := e.sigs
	white := e.white
	e.mu.RUnlock()

	var out []Threat
	for _, m := range enumModules(pid) {
		nameLower := strings.ToLower(m.Name)

		// The process's own executable image is not an injected module.
		if strings.HasSuffix(nameLower, ".exe") {
			continue
		}

		// Signature match on module name → definite.
		if sigs.matchModule(nameLower) {
			out = append(out, Threat{
				Kind: KindKnownHackModule, Severity: SevHigh, Name: m.Name,
				Path: m.Path, PID: pid, Reason: "알려진 핵 모듈 시그니처와 일치",
			})
			continue
		}

		// Heuristic: a module loaded from outside trusted locations and not
		// validly signed is a likely injected cheat.
		if white.trustedPath(m.Path) {
			continue // game/system/battle.net dirs are fine
		}
		if isTrustedSigned(m.Path) {
			continue // signed by a trusted publisher, tolerate even if out-of-dir
		}
		out = append(out, Threat{
			Kind: KindInjectedDLL, Severity: SevHigh, Name: m.Name, Path: m.Path,
			PID: pid, Reason: "게임 프로세스에 주입된 미서명/비정상 경로 DLL",
		})
	}
	return dedupeSort(out)
}

// ScanProcesses inspects all running processes for known hack signatures and
// suspicious unsigned processes exposing hack-like windows.
func (e *Engine) ScanProcesses() []Threat {
	e.mu.RLock()
	sigs := e.sigs
	e.mu.RUnlock()

	var out []Threat
	procs := enumProcesses()
	windows := enumWindowTitles() // pid -> []title

	for _, p := range procs {
		nameLower := strings.ToLower(p.Name)
		if sigs.matchProcess(nameLower) {
			out = append(out, Threat{
				Kind: KindKnownHackProc, Severity: SevHigh, Name: p.Name,
				Path: p.Path, PID: p.PID, Reason: "알려진 핵 프로세스 시그니처와 일치",
			})
			continue
		}
		// Window-title signature (many trainers/maphacks have telltale titles).
		for _, title := range windows[p.PID] {
			if sigs.matchWindow(strings.ToLower(title)) {
				out = append(out, Threat{
					Kind: KindSuspiciousProc, Severity: SevMedium, Name: p.Name,
					Path: p.Path, PID: p.PID,
					Reason: "핵으로 의심되는 창 제목: " + title,
				})
				break
			}
		}
	}
	return dedupeSort(out)
}

func dedupeSort(in []Threat) []Threat {
	seen := map[string]bool{}
	var out []Threat
	for _, t := range in {
		key := string(t.Kind) + "|" + strings.ToLower(t.Name) + "|" + t.Path
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Severity > out[j].Severity
	})
	return out
}
