# Build hlauncher (32-bit, windowsgui) and stage a runnable dist folder.
# Usage:  powershell -ExecutionPolicy Bypass -File build\build.ps1
$ErrorActionPreference = "Stop"

# --- Go environment (adjust GOROOT if you install Go elsewhere) ---
if (-not $env:GOROOT) { $env:GOROOT = "C:\Users\han\go-sdk\go" }
if (-not $env:GOPATH) { $env:GOPATH = "C:\Users\han\go" }
$env:PATH = "$env:GOROOT\bin;$env:GOPATH\bin;$env:PATH"
$env:GOOS = "windows"
$env:GOARCH = "386"
$env:CGO_ENABLED = "0"

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$version   = "0.1.0"
$buildMode = "production"
$pkg       = "github.com/hlauncher/hlauncher/internal/version"
$ldflags   = "-s -w -H=windowsgui " +
             "-X '$pkg.Version=$version' " +
             "-X '$pkg.BuildMode=$buildMode'"

$dist = Join-Path $root "dist"
New-Item -ItemType Directory -Force -Path $dist | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $dist "Library") | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $dist "Etc") | Out-Null

Write-Host "Building hlauncher.exe (386, $buildMode $version)..."
go build -trimpath -ldflags $ldflags -o (Join-Path $dist "hlauncher.exe") ./cmd/hlauncher
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# Stage native assets next to the exe.
Copy-Item (Join-Path $root "Library\*") (Join-Path $dist "Library") -Force -Recurse
Copy-Item (Join-Path $root "Etc\*")     (Join-Path $dist "Etc")     -Force -Recurse

Write-Host "Done. Output:"
Get-ChildItem $dist -Recurse -File |
  Select-Object @{n='file';e={$_.FullName.Replace($dist,'')}}, Length |
  Format-Table -AutoSize | Out-String | Write-Host
