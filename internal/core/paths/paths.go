// Package paths resolves the on-disk locations hlauncher needs: the bundled
// native assets (kDetector, injector, Battle.net dll) that live next to the
// executable, and the per-user data directory under %APPDATA%.
package paths

import (
	"os"
	"path/filepath"

	"github.com/hlauncher/hlauncher/internal/version"
)

// AppPaths holds every resolved path the launcher uses.
type AppPaths struct {
	// ExeDir is the directory containing hlauncher.exe.
	ExeDir string
	// LibraryDir is ExeDir\Library, holding the native assets.
	LibraryDir string
	// EtcDir is ExeDir\Etc, holding fonts / sounds.
	EtcDir string
	// DataDir is %APPDATA%\hlauncher.
	DataDir string
	// LogsDir is DataDir\logs.
	LogsDir string
	// ChatLogsDir is where StarCraft chat logs are read from (set by config).
	ChatLogsDir string

	// Native assets.
	DetectorPath    string // Library\kDetector.k  (injected into StarCraft)
	InjectorDLLPath string // Library\injector.dll (FFI injection helper)
	BattleNetDLLPath string // Library\Battle.net.dll (injected into Battle.net)
	D3DCompilerPath string // Library\d3dcompiler_43.dll (kDetector dependency)
}

// New resolves all paths relative to the running executable and %APPDATA%.
// It does not create anything; call EnsureDirectoriesExist for that.
func New() (*AppPaths, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// Resolve symlinks so paths are stable.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	exeDir := filepath.Dir(exe)

	appData := getAppDataPath()
	dataDir := filepath.Join(appData, version.AppName)

	lib := filepath.Join(exeDir, "Library")
	p := &AppPaths{
		ExeDir:     exeDir,
		LibraryDir: lib,
		EtcDir:     filepath.Join(exeDir, "Etc"),
		DataDir:    dataDir,
		LogsDir:    filepath.Join(dataDir, "logs"),

		DetectorPath:     filepath.Join(lib, "kDetector.k"),
		InjectorDLLPath:  filepath.Join(lib, "injector.dll"),
		BattleNetDLLPath: filepath.Join(lib, "Battle.net.dll"),
		D3DCompilerPath:  filepath.Join(lib, "d3dcompiler_43.dll"),
	}
	return p, nil
}

// EnsureDirectoriesExist creates the per-user data/log directories.
func (p *AppPaths) EnsureDirectoriesExist() error {
	for _, d := range []string{p.DataDir, p.LogsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// ConfigFile returns the path to the JSON config file.
func (p *AppPaths) ConfigFile() string {
	return filepath.Join(p.DataDir, "config.json")
}

// CheckDetectorExists reports whether kDetector.k is present.
func (p *AppPaths) CheckDetectorExists() bool { return fileExists(p.DetectorPath) }

// CheckInjectorDLLExists reports whether injector.dll is present.
func (p *AppPaths) CheckInjectorDLLExists() bool { return fileExists(p.InjectorDLLPath) }

// CheckBattleNetDllExists reports whether Battle.net.dll is present.
func (p *AppPaths) CheckBattleNetDllExists() bool { return fileExists(p.BattleNetDLLPath) }

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func getAppDataPath() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}
	// Fallback: %USERPROFILE%\AppData\Roaming
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Roaming")
}
