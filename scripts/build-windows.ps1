param(
    [switch]$DebugConsole
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
$PatchedDir = Join-Path $RepoRoot "build\patched-go"
$WorkFile = Join-Path $PatchedDir "xdial.work"
$WinWorkFile = Join-Path $PatchedDir "xdial-windows.work"
$OutDir = Join-Path $RepoRoot "build\windows"
$OutBinary = Join-Path $OutDir "XDial.exe"

$env:GOTOOLCHAIN = "auto"

# 1. Ensure patched sing-box / tailscale / sslcon workspace is up to date
$GitBash = "C:\Program Files\Git\bin\bash.exe"
if (-not (Test-Path $GitBash)) {
    throw "Git Bash not found at $GitBash"
}
Set-Location $RepoRoot
& $GitBash scripts/prepare-patched-go-mod.sh --check 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "[1/3] Preparing patched Go workspace..."
    & $GitBash scripts/prepare-patched-go-mod.sh
    if ($LASTEXITCODE -ne 0) { throw "prepare-patched-go-mod.sh failed" }
} else {
    Write-Host "[1/3] Patched Go workspace is current."
}

Copy-Item $WorkFile $WinWorkFile -Force
$env:GOWORK = "off"
go work edit -use="../../windows" $WinWorkFile

# 2. Build React + TypeScript Frontend
Write-Host "[2/3] Building Windows React/TS frontend..."
Set-Location (Join-Path $RepoRoot "windows\frontend")
if (-not (Test-Path "node_modules")) {
    npm install
    if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
}
npm run build
if ($LASTEXITCODE -ne 0) { throw "Frontend build failed" }

# 3. Build Windows Wails v3 + Libbox Single Executable
Write-Host "[3/3] Compiling XDial.exe..."
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
Set-Location (Join-Path $RepoRoot "windows")
$env:GOWORK = $WinWorkFile

$BaseVersion = "v0.9.9"
$GitDesc = (git -C $RepoRoot rev-parse --short HEAD 2>$null)
$DirtySuffix = ""
$StatusOut = (git -C $RepoRoot status --porcelain 2>$null)
if ($StatusOut) { $DirtySuffix = "+dev" }
if ($GitDesc) {
    $VersionTag = "$BaseVersion ($GitDesc$DirtySuffix)"
} else {
    $VersionTag = "$BaseVersion-windows"
}

$LdFlags = "-s -w -X 'main.version=$VersionTag'"
if (-not $DebugConsole) {
    $LdFlags = "-H=windowsgui $LdFlags"
}

go build -tags "with_gvisor,with_utls,production" -ldflags $LdFlags -o $OutBinary .
if ($LASTEXITCODE -ne 0) { throw "Go build failed" }

$ZipPath = Join-Path $OutDir "XDial-windows-amd64.zip"
$FilesToPack = @($OutBinary)
$WintunDll = Join-Path $OutDir "wintun.dll"
if (Test-Path $WintunDll) {
    $FilesToPack += $WintunDll
}
Compress-Archive -Path $FilesToPack -DestinationPath $ZipPath -Force

Write-Host "Build succeeded: $OutBinary"
Write-Host "Package ready:   $ZipPath"
