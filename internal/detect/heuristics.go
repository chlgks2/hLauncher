package detect

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hlauncher/hlauncher/internal/core/loader"
)

// whitelist decides whether a module path is in a trusted location.
type whitelist struct {
	trustedDirs []string // lowercased, cleaned, trailing-separator dirs
}

func newWhitelist(gameDirs []string) *whitelist {
	var dirs []string
	// System directories.
	if w := os.Getenv("SystemRoot"); w != "" {
		dirs = append(dirs, w) // C:\Windows (covers System32, WinSxS, etc.)
	} else {
		dirs = append(dirs, `C:\Windows`)
	}
	// Program Files (Blizzard/Battle.net installers, common runtimes).
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432"} {
		if v := os.Getenv(env); v != "" {
			dirs = append(dirs, v)
		}
	}
	// Game / launcher-provided directories (StarCraft, Battle.net installs).
	dirs = append(dirs, gameDirs...)

	norm := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		c := strings.ToLower(filepath.Clean(d))
		norm = append(norm, c+string(filepath.Separator))
	}
	return &whitelist{trustedDirs: norm}
}

// trustedPath reports whether path lives under a trusted directory.
func (w *whitelist) trustedPath(path string) bool {
	if path == "" {
		return false
	}
	p := strings.ToLower(filepath.Clean(path)) + ""
	for _, d := range w.trustedDirs {
		if strings.HasPrefix(p+string(filepath.Separator), d) || strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// isTrustedSigned reports whether the file at path carries a valid Authenticode
// signature. A validly signed DLL (even outside a trusted dir) is tolerated to
// reduce false positives on legitimate overlays/tools.
func isTrustedSigned(path string) bool {
	if path == "" {
		return false
	}
	return loader.VerifyAuthenticode(path) == nil
}
