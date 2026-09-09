package detect

import (
	"encoding/json"
	"os"
	"strings"
)

// Signatures is a database of known-hack indicators. All matching is done in
// lowercase, substring-based (an entry matches if it appears within the name).
type Signatures struct {
	// Process executable names, e.g. "maphack.exe", "starcrafttrainer".
	Processes []string `json:"processes"`
	// Injected module (DLL) names, e.g. "maphack.dll".
	Modules []string `json:"modules"`
	// Window-title substrings, e.g. "map hack", "auto mine".
	Windows []string `json:"windows"`
}

// LoadSignatures reads a signature DB from a JSON file. A missing file yields
// empty (heuristics still work).
func LoadSignatures(path string) (Signatures, error) {
	var s Signatures
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	return s.normalized(), nil
}

// DefaultSignatures returns a small built-in seed list. Extend via the JSON DB.
func DefaultSignatures() Signatures {
	return Signatures{
		// Deliberately conservative; real entries added as hacks are identified.
		Processes: []string{},
		Modules:   []string{},
		Windows:   []string{},
	}.normalized()
}

func (s Signatures) normalized() Signatures {
	return Signatures{
		Processes: lowerAll(s.Processes),
		Modules:   lowerAll(s.Modules),
		Windows:   lowerAll(s.Windows),
	}
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(strings.ToLower(v))
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s Signatures) matchProcess(nameLower string) bool { return containsAny(nameLower, s.Processes) }
func (s Signatures) matchModule(nameLower string) bool  { return containsAny(nameLower, s.Modules) }
func (s Signatures) matchWindow(titleLower string) bool { return containsAny(titleLower, s.Windows) }

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}
