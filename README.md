# hlauncher

StarCraft 안티치트 런처. 단종된 kLauncher(4.1.1)를 리버스 엔지니어링하여, 검증된
네이티브 자산(kDetector.k / injector.dll / Battle.net.dll)은 그대로 재사용하고
런처 셸(오케스트레이션 계층)만 Go로 새로 작성했습니다. **개인용**입니다.

## 무엇을 하는가

1. Battle.net / StarCraft 프로세스를 감시하고
2. StarCraft가 실행되면 핵 탐지 엔진 `kDetector.k`를 게임 프로세스에 주입하며
3. 시스템 트레이에서 상태를 보여주고 게임 실행/수동 주입을 제어합니다.

원본은 웹 UI(`start.klauncher.kr`)를 브라우저 렌더러에 띄우는 구조였지만,
hlauncher는 **네이티브 트레이 GUI**(lxn/walk)로 대체했습니다.

## 아키텍처

```
                    ┌──────────────── hlauncher.exe (Go, 32-bit) ───────────────┐
  트레이 GUI(walk) ─┤  app(오케스트레이션)                                       │
                    │   ├─ monitor : 프로세스/모듈 감시 (Toolhelp32)             │
                    │   ├─ registry: 설치 경로 자동 탐지 (HKLM Uninstall)        │
                    │   ├─ config  : %APPDATA%\hlauncher\config.json             │
                    │   ├─ loader  : WinVerifyTrust 서명 검증                    │
                    │   └─ inject  : 인젝션 (아래 두 백엔드)                      │
                    └────────────────────────────────────────────────────────────┘
                                          │ 주입
   ┌──────────────────────────────────────┼───────────────────────────────┐
   ▼                                       ▼                               ▼
 StarCraft.exe ← kDetector.k        Battle.net.exe ← Battle.net.dll   (injector.dll: FFI 백엔드)
 (탐지 엔진)                        (선택/고급, 파이프 미구현)
```

### 인젝션 백엔드
- **Native (기본)** — Go로 구현한 `OpenProcess(0x3A)` + `VirtualAllocEx` +
  `WriteProcessMemory` + `CreateRemoteThread(LoadLibraryW)`. 원본 Rust `injector.dll`과
  동일한 고전적 기법.
- **injector.dll (호환)** — 번들된 원본 `injector.dll`의 `ffi_execute_injection`을 호출.
  `app.SetBackend(app.BackendInjectorDLL)`로 선택.

## 빌드

Go 1.27+ 필요. **32비트(386)로 빌드해야** 32비트 `injector.dll` / `kDetector.k`를
다룰 수 있습니다.

```powershell
# 전체 빌드 + dist 폴더 구성 (exe + Library + Etc)
powershell -ExecutionPolicy Bypass -File build\build.ps1
```

수동 빌드:
```powershell
$env:GOOS="windows"; $env:GOARCH="386"; $env:CGO_ENABLED="0"
go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist\hlauncher.exe ./cmd/hlauncher
```

진단 도구(콘솔) — 게임 감지/경로 탐지 확인:
```powershell
go build -o dist\hldiag.exe ./cmd/hldiag ; .\dist\hldiag.exe
```

## 배포 폴더 구조 (dist)

```
hlauncher.exe
Library\  kDetector.k  injector.dll  Battle.net.dll  d3dcompiler_43.dll
Etc\      (폰트, 사운드)
```
`hlauncher.exe`는 자신의 폴더 옆 `Library\`에서 자산을 찾습니다.

## 현재 상태 (v0.1)

| 기능 | 상태 |
|------|------|
| 단일 인스턴스 / 트레이 GUI / 설정·경로 자동탐지 | ✅ |
| 프로세스·모듈 감시, 상태 표시 | ✅ |
| kDetector 직접 주입 (Native + injector.dll) | ✅ |
| 서명 검증 로더 | ✅ |
| Battle.net.dll ↔ 런처 named pipe 프로토콜 | ⬜ (v0.2) |
| 게임 분석 / 채팅 로그 / 광고차단 감지 / 자동 업데이트 | ⬜ |

### v0.2 이후 남은 작업
- `\\.\pipe\BattleNetPipe` 프레임 프로토콜 리버싱 → Battle.net.dll에 `set_detector_path`
  전달(원본과 동일하게 Battle.net 경유로 스타 실행 시점 주입).
- 실제 게임에서 주입 검증 및 kDetector 의존 DLL(d3dcompiler) 로드 경로 확인.

## 주의
개인 사용 목적의 리버스 엔지니어링 결과물입니다. `kDetector.k`는 VMProtect로 보호된
원본 탐지 엔진으로, 재작성 불가하여 그대로 재사용합니다.
