// Package registry reads Blizzard install locations from the Windows registry,
// mirroring how the original launcher discovered Battle.net and StarCraft.
package registry

import (
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// Well-known uninstall keys that carry an InstallLocation value.
const (
	battleNetUninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Battle.net`
	starcraftUninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\StarCraft`
)

// Reader queries the registry for game install paths.
type Reader struct{}

// NewReader returns a registry Reader.
func NewReader() *Reader { return &Reader{} }

// GetStringValue reads a string value from HKLM\<keyPath>\<valueName>.
// It tries both the 64-bit and 32-bit (WOW6432Node) registry views.
func (r *Reader) GetStringValue(keyPath, valueName string) (string, error) {
	for _, access := range []uint32{
		registry.QUERY_VALUE | registry.WOW64_64KEY,
		registry.QUERY_VALUE | registry.WOW64_32KEY,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, keyPath, access)
		if err != nil {
			continue
		}
		val, _, err := k.GetStringValue(valueName)
		k.Close()
		if err == nil && val != "" {
			return val, nil
		}
	}
	return "", registry.ErrNotExist
}

// GetBattleNetPath returns the Battle.net install directory, or "" if not found.
func (r *Reader) GetBattleNetPath() string {
	if v, err := r.GetStringValue(battleNetUninstallKey, "InstallLocation"); err == nil {
		return v
	}
	return ""
}

// GetBattleNetExe returns the full path to Battle.net.exe, or "" if not found.
func (r *Reader) GetBattleNetExe() string {
	dir := r.GetBattleNetPath()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "Battle.net.exe")
}

// GetStarCraftInstalledPath returns the StarCraft install directory, or "".
func (r *Reader) GetStarCraftInstalledPath() string {
	if v, err := r.GetStringValue(starcraftUninstallKey, "InstallLocation"); err == nil {
		return v
	}
	return ""
}
