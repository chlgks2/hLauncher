// Package app wires the launcher together: it owns configuration, path
// resolution, the process monitor, and the injection logic, and exposes a
// small API for the UI layer to drive.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hlauncher/hlauncher/internal/blacklist"
	"github.com/hlauncher/hlauncher/internal/config"
	"github.com/hlauncher/hlauncher/internal/core/loader"
	"github.com/hlauncher/hlauncher/internal/core/monitor"
	"github.com/hlauncher/hlauncher/internal/core/paths"
	"github.com/hlauncher/hlauncher/internal/core/pipe"
	"github.com/hlauncher/hlauncher/internal/detect"
	"github.com/hlauncher/hlauncher/internal/identity"
	"github.com/hlauncher/hlauncher/internal/inject"
	"github.com/hlauncher/hlauncher/internal/launch"
	"github.com/hlauncher/hlauncher/internal/registry"
	"github.com/hlauncher/hlauncher/internal/roster"
	"github.com/hlauncher/hlauncher/internal/version"
)

// InjectBackend selects how DLLs are injected into the games.
type InjectBackend int

const (
	// BackendNative uses hlauncher's built-in Go injector (default).
	BackendNative InjectBackend = iota
	// BackendInjectorDLL uses the bundled injector.dll (original-compatible).
	BackendInjectorDLL
)

// Module file names used both as injection targets and injection markers.
const (
	moduleDetector  = "kDetector.k"
	moduleBattleNet = "Battle.net.dll"
)

// StarCraft launch arguments. Battle.net launches the game as
// `StarCraft.exe -launch -uid s1`; passing the same args to the 32-bit binary
// makes it grab the running Battle.net session and start normally (verified).
// "s1" is StarCraft's Blizzard product code.
var starcraftLaunchArgs = []string{"-launch", "-uid", "s1"}

// App is the central launcher controller.
type App struct {
	Paths  *paths.AppPaths
	Config *config.Manager
	Reg    *registry.Reader
	Loader *loader.DllLoader
	Mon    *monitor.Monitor
	Pipe   *pipe.KLauncherPipe
	Detect   *detect.Engine
	Black    *blacklist.List
	Identity *identity.Store

	backend  InjectBackend
	injector *inject.InjectorDLL // non-nil only for BackendInjectorDLL

	mu            sync.Mutex
	last          monitor.Status
	injectedSC    bool   // kDetector successfully injected into the current SC process
	injectedBN    bool   // Battle.net.dll successfully injected into the current BN process
	attemptedSC   uint32 // SC PID we have already auto-attempted (0 = none), to avoid retry spam
	attemptedBN   uint32 // BN PID we have already auto-attempted
	scPID         uint32
	bnPID         uint32
	autoBattleNet bool // also inject Battle.net.dll (advanced)

	onStatus func(monitor.Status)
	onLog    func(level, msg string)
	onThreat func([]detect.Threat)
	onRoster func([]roster.Participant, []BlackHit, bool) // bool = frozen (in-game snapshot)

	lastLobby []roster.Participant // last live lobby roster, fallback while loading

	lastInGame     []roster.Participant // cached in-game engine roster
	lastInGameScan time.Time            // when lastInGame was last scanned

	pubParts  []roster.Participant // last roster published to the UI
	pubHits   []BlackHit
	pubFrozen bool

	detectStop chan struct{}
	detectDone chan struct{}
	seenThreat map[string]bool

	roomStop  chan struct{}
	roomDone  chan struct{}
	seenBlack map[string]bool // per-room-session: alerted black identifiers
	lastSCPID uint32

	capStop chan struct{}
	capDone chan struct{}
}

// BlackHit is a matched blacklisted participant currently in the room.
type BlackHit struct {
	Participant roster.Participant
	Entry       blacklist.Entry
}

// New constructs the App and all its subsystems. It does not start monitoring;
// call Start for that.
func New() (*App, error) {
	p, err := paths.New()
	if err != nil {
		return nil, fmt.Errorf("app: paths: %w", err)
	}
	if err := p.EnsureDirectoriesExist(); err != nil {
		return nil, fmt.Errorf("app: mkdir: %w", err)
	}
	cfg, err := config.NewManager(p.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("app: config: %w", err)
	}

	a := &App{
		Paths:  p,
		Config: cfg,
		Reg:    registry.NewReader(),
		// Do not require signatures by default: kDetector.k is renamed and may
		// not verify as an image; the injected assets are trusted local files.
		Loader:  loader.New(false),
		Mon:     monitor.New(1500*time.Millisecond, moduleBattleNet, moduleDetector),
		backend: BackendNative,
	}
	a.Mon.AddStatusHandler(a.onStatusChange)

	// Detection engine (stage 1: external hack detection). Trusted dirs come
	// from the game/Battle.net install locations plus system/Program Files.
	var gameDirs []string
	if d := a.Reg.GetStarCraftInstalledPath(); d != "" {
		gameDirs = append(gameDirs, d)
	}
	if d := a.Reg.GetBattleNetPath(); d != "" {
		gameDirs = append(gameDirs, d)
	}
	sigs := detect.DefaultSignatures()
	if s, err := detect.LoadSignatures(a.signaturesPath()); err == nil {
		sigs = s
	}
	a.Detect = detect.New(sigs, gameDirs)
	a.seenThreat = make(map[string]bool)
	a.detectStop = make(chan struct{})
	a.detectDone = make(chan struct{})

	// Blacklist (battleTag-keyed, with reason + time), for room-participant alerts.
	bl, err := blacklist.Load(a.blacklistPath())
	if err != nil {
		a.logf("warn", "블랙리스트 로드 실패: %v", err)
		bl, _ = blacklist.Load("") // empty
	}
	a.Black = bl
	a.seenBlack = make(map[string]bool)
	a.roomStop = make(chan struct{})
	a.roomDone = make(chan struct{})
	a.capStop = make(chan struct{})
	a.capDone = make(chan struct{})

	// Identity store: persistent name->battleTag map, accumulated from channel
	// events so players are trackable by unique battleTag over time.
	idStore, err := identity.Load(filepath.Join(p.DataDir, "identities.json"))
	if err != nil {
		a.logf("warn", "identity 로드 실패: %v", err)
		idStore, _ = identity.Load("")
	}
	a.Identity = idStore

	// KLauncherPipe server: kDetector connects here after injection and expects
	// the set_detector_path / send_token handshake, or it terminates the game.
	exePath, _ := launch.CurrentExecutablePath()
	a.Pipe = pipe.NewKLauncherPipe(pipe.HandshakeInfo{
		LauncherPath: exePath,
		AppDataPath:  p.DataDir,
		Version:      version.Version,
		UserToken:    "",
		UserNickName: "",
	}, a.logf2)
	a.Pipe.OnMessage(a.onPipeMessage)
	return a, nil
}

// logf2 adapts the app logger to the pipe.Logf signature.
func (a *App) logf2(format string, args ...interface{}) {
	a.logf("info", format, args...)
}

// onPipeMessage handles messages kDetector sends to the launcher.
func (a *App) onPipeMessage(msg pipe.Message) {
	a.logf("info", "kDetector -> %s", msg.Command)
}

// SetBackend chooses the injection backend. For BackendInjectorDLL the bundled
// injector.dll is loaded lazily on first injection.
func (a *App) SetBackend(b InjectBackend) { a.backend = b }

// SetAutoBattleNet enables/disables also injecting Battle.net.dll (advanced;
// requires the pipe protocol to hand kDetector's path to Battle.net.dll — not
// yet implemented, so this only performs the injection itself).
func (a *App) SetAutoBattleNet(v bool) { a.autoBattleNet = v }

// OnStatus registers a UI callback for status changes.
func (a *App) OnStatus(fn func(monitor.Status)) { a.onStatus = fn }

// OnLog registers a UI/log callback. level is "info"|"warn"|"error".
func (a *App) OnLog(fn func(level, msg string)) { a.onLog = fn }

// OnThreat registers a UI callback fired when new threats are detected.
func (a *App) OnThreat(fn func([]detect.Threat)) { a.onThreat = fn }

// OnRoster registers a UI callback fired on each room scan with the current
// participants and any blacklisted players currently present. The final bool is
// true when the list is a frozen in-game snapshot (last lobby, not live).
func (a *App) OnRoster(fn func([]roster.Participant, []BlackHit, bool)) { a.onRoster = fn }

func (a *App) blacklistPath() string {
	return filepath.Join(a.Paths.DataDir, "blacklist.json")
}

// AddBlack blacklists a participant: uses their battleTag as the key when known,
// otherwise their display name. Reason and time are recorded.
func (a *App) AddBlack(battleTag, name, reason string) error {
	id := battleTag
	if id == "" {
		id = name
	}
	if id == "" {
		return fmt.Errorf("식별자(배틀태그 또는 이름)가 필요합니다")
	}
	if err := a.Black.Add(id, name, reason); err != nil {
		return err
	}
	a.logf("info", "블랙 등록: %s (%s)", id, reason)
	return nil
}

// RemoveBlack removes a blacklist entry by its identifier.
func (a *App) RemoveBlack(id string) error { return a.Black.Remove(id) }

// CurrentRoster reads the current room participants (empty if not in a room).
func (a *App) CurrentRoster() []roster.Participant {
	st := a.Status()
	if !st.StarCraft.Running {
		return nil
	}
	parts, _, err := roster.Read(st.StarCraft.PID, a.Identity)
	if err != nil {
		return nil
	}
	_ = a.Identity.Save()
	return parts
}

// Displayed returns the roster the scan loop last published — the live lobby, or
// a frozen last-lobby snapshot while in-game — with its blacklist hits and the
// frozen flag. The UI uses this so a manual refresh matches the auto view.
func (a *App) Displayed() ([]roster.Participant, []BlackHit, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pubParts, a.pubHits, a.pubFrozen
}

// roomLoop periodically reads the room roster and alerts on blacklisted players.
func (a *App) roomLoop() {
	defer close(a.roomDone)
	const (
		fast = 3 * time.Second  // in a lobby: refresh participants often
		mid  = 6 * time.Second  // in the chat channel: keep harvesting battleTags
		slow = 20 * time.Second // in an actual match / no data: near-idle probe
	)
	interval := fast
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-a.roomStop:
			return
		case <-timer.C:
			// Pace by screen: fast in a lobby, medium in the channel (so we still
			// capture join events), slow in-game where no lobby/chat data exists.
			switch a.scanRoom() {
			case roster.StateLobby:
				interval = fast
			case roster.StateChannel:
				interval = mid
			default:
				interval = slow
			}
			timer.Reset(interval)
		}
	}
}

// scanRoom performs one room scan and returns the screen state, which the room
// loop uses to pace itself (idle == in an actual match: back off hard).
//
// Live lobby data is destroyed the moment a match starts, so we keep the last
// lobby roster and re-display it (frozen) in-game — that's the only way to show
// participants/battleTags during play, and it's what the original launcher did.
func (a *App) scanRoom() roster.State {
	st := a.Status()
	if !st.StarCraft.Running {
		a.mu.Lock()
		a.lastLobby, a.lastInGame = nil, nil // game gone: drop caches
		a.mu.Unlock()
		return roster.StateIdle
	}
	// Reset per-room alert memory (and caches) when the process changes.
	if st.StarCraft.PID != a.lastSCPID {
		a.lastSCPID = st.StarCraft.PID
		a.mu.Lock()
		a.seenBlack = make(map[string]bool)
		a.lastLobby, a.lastInGame = nil, nil
		a.mu.Unlock()
	}

	parts, state, err := roster.Read(st.StarCraft.PID, a.Identity)
	if err != nil {
		return roster.StateIdle
	}
	_ = a.Identity.Save() // persist any newly-captured battleTags

	switch state {
	case roster.StateLobby:
		// Live room: remember it as the snapshot and show it live. Drop any
		// previous match's in-game cache — a new game is being set up.
		a.mu.Lock()
		a.lastLobby = parts
		a.lastInGame = nil
		a.mu.Unlock()
		a.publishRoster(parts, false)
	default:
		// No lobby (StateIdle in an actual match/menu, or StateChannel between
		// rooms). Read the engine's in-game player array directly (live names +
		// battleTags, no injection) — a match keeps it even after the lobby JSON
		// is gone. The array is stable during a match and the scan is heavy, so
		// throttle it (retry sooner while we have nothing, to catch game start).
		a.mu.Lock()
		wait := 25 * time.Second
		if len(a.lastInGame) == 0 {
			wait = 8 * time.Second
		}
		due := time.Since(a.lastInGameScan) > wait
		a.mu.Unlock()
		if due {
			ig, _ := roster.InGamePlayers(st.StarCraft.PID, a.Identity)
			a.mu.Lock()
			a.lastInGame = ig
			a.lastInGameScan = time.Now()
			a.mu.Unlock()
		}
		a.mu.Lock()
		ig, snap := a.lastInGame, a.lastLobby
		a.mu.Unlock()
		switch {
		case len(ig) > 0:
			state = roster.StateInGame
			a.publishRoster(a.resolveTags(ig), true) // live in-game roster
		case state == roster.StateChannel:
			a.publishRoster(nil, false) // in the channel, no room to show
		case len(snap) > 0:
			a.publishRoster(a.resolveTags(snap), true) // loading: frozen last lobby
		default:
			a.publishRoster(nil, false)
		}
	}
	return state
}

// resolveTags re-fills battleTags on a snapshot from the current identity store.
func (a *App) resolveTags(parts []roster.Participant) []roster.Participant {
	out := make([]roster.Participant, len(parts))
	copy(out, parts)
	for i := range out {
		if out[i].BattleTag == "" {
			if rec, ok := a.Identity.LookupName(out[i].Name); ok {
				out[i].BattleTag = rec.BattleTag
				out[i].ToonID = rec.Toon
			}
		}
	}
	return out
}

// publishRoster matches the blacklist, fires the UI callback, and logs alerts.
func (a *App) publishRoster(parts []roster.Participant, frozen bool) {
	var hits []BlackHit
	for _, p := range parts {
		if e, ok := a.Black.Match(p.BattleTag, p.Name); ok {
			hits = append(hits, BlackHit{Participant: p, Entry: e})
		}
	}
	a.mu.Lock()
	a.pubParts, a.pubHits, a.pubFrozen = parts, hits, frozen
	a.mu.Unlock()
	if a.onRoster != nil {
		a.onRoster(parts, hits, frozen)
	}
	for _, h := range hits {
		id := h.Entry.BattleTag
		a.mu.Lock()
		fresh := !a.seenBlack[id]
		a.seenBlack[id] = true
		a.mu.Unlock()
		if fresh {
			reason := h.Entry.Reason
			if reason == "" {
				reason = "(사유 없음)"
			}
			a.logf("error", "⚠ 블랙유저 발견: %s  핑%dms  사유: %s",
				h.Participant.Name, h.Participant.Latency, reason)
		}
	}
}

// signaturesPath returns the location of the hack-signature DB.
func (a *App) signaturesPath() string {
	return filepath.Join(a.Paths.DataDir, "signatures.json")
}

// detectLoop periodically scans for external hacks while a game is running.
func (a *App) detectLoop() {
	defer close(a.detectDone)
	t := time.NewTicker(4 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-a.detectStop:
			return
		case <-t.C:
			a.runDetection()
		}
	}
}

// runDetection performs one scan pass and reports any newly-seen threats.
func (a *App) runDetection() {
	st := a.Status()
	// Only scan actively while a game is present (keeps overhead low).
	if !st.StarCraft.Running && !st.BattleNet.Running {
		return
	}
	var threats []detect.Threat
	threats = append(threats, a.Detect.ScanProcesses()...)
	if st.StarCraft.Running {
		threats = append(threats, a.Detect.ScanGame(st.StarCraft.PID)...)
		threats = append(threats, a.Detect.ScanExternalAccess(st.StarCraft.PID)...)
	}
	a.reportThreats(threats)
}

// reportThreats fires the UI callback for threats not seen before.
func (a *App) reportThreats(threats []detect.Threat) {
	if len(threats) == 0 {
		return
	}
	a.mu.Lock()
	var fresh []detect.Threat
	for _, t := range threats {
		key := string(t.Kind) + "|" + t.Name + "|" + t.Path
		if !a.seenThreat[key] {
			a.seenThreat[key] = true
			fresh = append(fresh, t)
		}
	}
	a.mu.Unlock()
	if len(fresh) == 0 {
		return
	}
	for _, t := range fresh {
		a.logf("warn", "위협 감지 [%s] %s (%s)", t.Severity, t.Name, t.Reason)
	}
	if a.onThreat != nil {
		a.onThreat(fresh)
	}
}

// RunDetectionNow triggers an immediate scan (for a manual "scan now" action).
func (a *App) RunDetectionNow() []detect.Threat {
	st := a.Status()
	var threats []detect.Threat
	threats = append(threats, a.Detect.ScanProcesses()...)
	if st.StarCraft.Running {
		threats = append(threats, a.Detect.ScanGame(st.StarCraft.PID)...)
		threats = append(threats, a.Detect.ScanExternalAccess(st.StarCraft.PID)...)
	}
	if st.BattleNet.Running {
		threats = append(threats, a.Detect.ScanGame(st.BattleNet.PID)...)
	}
	return threats
}

func (a *App) logf(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if a.onLog != nil {
		a.onLog(level, msg)
	}
}

// Start begins process monitoring and the pipe server.
func (a *App) Start() {
	a.logf("info", "hlauncher started; monitoring for Battle.net / StarCraft")
	if !a.Paths.CheckDetectorExists() {
		a.logf("warn", "detector asset missing: %s", a.Paths.DetectorPath)
	}
	if err := a.Pipe.Listen(); err != nil {
		a.logf("error", "KLauncherPipe listen failed: %v", err)
	}
	a.Mon.Start()
	go a.detectLoop()
	go a.roomLoop()
}

// Stop halts monitoring, detection, room scanning, and the pipe server.
func (a *App) Stop() {
	a.Mon.Stop()
	if a.detectStop != nil {
		close(a.detectStop)
		<-a.detectDone
	}
	if a.roomStop != nil {
		close(a.roomStop)
		<-a.roomDone
	}
	if a.Pipe != nil {
		a.Pipe.Close()
	}
}

// Status returns the latest known status.
func (a *App) Status() monitor.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// IsAdmin reports whether hlauncher is elevated.
func (a *App) IsAdmin() bool { return launch.IsAdmin() }

// onStatusChange is called by the monitor whenever status changes. It resets
// per-process injection state when a game restarts and triggers auto-injection.
func (a *App) onStatusChange(st monitor.Status) {
	a.mu.Lock()
	// Detect process (re)starts by PID change → clear our injected/attempted flags.
	if st.StarCraft.PID != a.scPID {
		a.scPID = st.StarCraft.PID
		a.injectedSC = false
		a.attemptedSC = 0
	}
	if st.BattleNet.PID != a.bnPID {
		a.bnPID = st.BattleNet.PID
		a.injectedBN = false
		a.attemptedBN = 0
	}
	a.last = st
	a.mu.Unlock()

	if a.onStatus != nil {
		a.onStatus(st)
	}

	a.autoInject(st)
}

// autoInject injects the anti-cheat modules when the games appear and are not
// already injected.
func (a *App) autoInject(st monitor.Status) {
	cfg := a.Config.Get()
	if !cfg.AutoInject {
		return
	}

	// StarCraft → kDetector.k (primary path).
	if st.StarCraft.Running && !st.StarCraft.Injected {
		a.mu.Lock()
		skip := a.injectedSC || a.attemptedSC == st.StarCraft.PID
		if !skip {
			a.attemptedSC = st.StarCraft.PID // mark attempted now: one auto-try per PID
		}
		a.mu.Unlock()
		if !skip {
			pid := st.StarCraft.PID
			// Guard against the 64-bit/32-bit mismatch that would otherwise fail
			// on every tick. Report it once, clearly, without balloon spam.
			if ok, err := inject.IsProcess32Bit(pid); err == nil && !ok {
				a.logf("warn", "StarCraft(pid %d)가 64비트로 실행되었습니다. "+
					"kDetector(32비트) 주입 불가 — 트레이 메뉴의 'StarCraft(32비트) 실행'을 사용하세요.", pid)
			} else if err := a.InjectDetector(pid); err != nil {
				a.logf("error", "detector injection failed: %v", err)
			} else {
				a.mu.Lock()
				a.injectedSC = true
				a.mu.Unlock()
				a.logf("info", "kDetector injected into StarCraft (pid %d)", pid)
			}
		}
	}

	// Battle.net → Battle.net.dll (advanced/optional).
	if a.autoBattleNet && st.BattleNet.Running && !st.BattleNet.Injected {
		a.mu.Lock()
		skip := a.injectedBN || a.attemptedBN == st.BattleNet.PID
		if !skip {
			a.attemptedBN = st.BattleNet.PID
		}
		a.mu.Unlock()
		if !skip {
			if err := a.InjectBattleNet(st.BattleNet.PID); err != nil {
				a.logf("error", "Battle.net.dll injection failed: %v", err)
			} else {
				a.mu.Lock()
				a.injectedBN = true
				a.mu.Unlock()
				a.logf("info", "Battle.net.dll injected (pid %d)", st.BattleNet.PID)
			}
		}
	}
}

// InjectDetector injects kDetector.k into the given StarCraft PID. It verifies
// the target is 32-bit first, returning a clear error on a 64-bit mismatch.
func (a *App) InjectDetector(pid uint32) error {
	if !a.Paths.CheckDetectorExists() {
		return fmt.Errorf("detector asset missing: %s", a.Paths.DetectorPath)
	}
	if ok, err := inject.IsProcess32Bit(pid); err == nil && !ok {
		return fmt.Errorf("StarCraft(pid %d)가 64비트입니다. 32비트(x86)로 실행해야 kDetector를 주입할 수 있습니다", pid)
	}
	return a.doInject(pid, a.Paths.DetectorPath)
}

// InjectBattleNet injects Battle.net.dll into the given Battle.net PID.
func (a *App) InjectBattleNet(pid uint32) error {
	if !a.Paths.CheckBattleNetDllExists() {
		return fmt.Errorf("Battle.net.dll missing: %s", a.Paths.BattleNetDLLPath)
	}
	return a.doInject(pid, a.Paths.BattleNetDLLPath)
}

// doInject dispatches to the configured backend.
func (a *App) doInject(pid uint32, dllPath string) error {
	switch a.backend {
	case BackendInjectorDLL:
		if a.injector == nil {
			inj, err := inject.LoadInjectorDLL(a.Paths.InjectorDLLPath)
			if err != nil {
				return fmt.Errorf("load injector.dll: %w", err)
			}
			a.injector = inj
		}
		return a.injector.Inject(pid, dllPath)
	default:
		return inject.NativeInject(pid, dllPath)
	}
}

// --- launch controls (for the UI) ---

// LaunchBattleNet starts Battle.net.exe using the configured or detected path.
func (a *App) LaunchBattleNet() error {
	exe := a.resolveBattleNetExe()
	if exe == "" {
		return fmt.Errorf("Battle.net.exe path not set; choose it in settings")
	}
	pid, err := launch.Process(exe, nil, "")
	if err != nil {
		return err
	}
	a.logf("info", "launched Battle.net (pid %d): %s", pid, exe)
	return nil
}

// resolveBattleNetExe returns the configured path, else the registry-detected one.
func (a *App) resolveBattleNetExe() string {
	if p := a.Config.GetBNetPath(); p != "" {
		return p
	}
	return a.Reg.GetBattleNetExe()
}

// StarCraft32ExePath returns the path to the 32-bit StarCraft binary, or "".
func (a *App) StarCraft32ExePath() string {
	dir := a.Reg.GetStarCraftInstalledPath()
	if dir == "" {
		return ""
	}
	exe := filepath.Join(dir, "x86", "StarCraft.exe")
	if _, err := os.Stat(exe); err != nil {
		return ""
	}
	return exe
}

// LaunchStarCraft32 launches the 32-bit StarCraft binary directly so that the
// 32-bit kDetector can be injected. Requires Battle.net to be running/logged in.
func (a *App) LaunchStarCraft32() error {
	exe := a.StarCraft32ExePath()
	if exe == "" {
		return fmt.Errorf("32비트 StarCraft(x86\\StarCraft.exe)를 찾을 수 없습니다")
	}
	// Battle.net must be running/logged in for the session hand-off to succeed.
	pid, err := launch.Process(exe, starcraftLaunchArgs, filepath.Dir(exe))
	if err != nil {
		return err
	}
	a.logf("info", "launched 32-bit StarCraft (pid %d): %s %v", pid, exe, starcraftLaunchArgs)
	return nil
}

// DetectPaths fills in missing config paths from the registry. Returns what it found.
func (a *App) DetectPaths() (bnet string, starcraft string) {
	bnet = a.Reg.GetBattleNetExe()
	starcraft = a.Reg.GetStarCraftInstalledPath()
	return
}
