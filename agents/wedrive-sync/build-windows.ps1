$ErrorActionPreference = 'Stop'

$agentRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $agentRoot '..\..')
$outputRoot = Join-Path $repoRoot 'frontend\public\downloads'
$env:UV_CACHE_DIR = Join-Path $repoRoot '.uv-cache'
$buildMutex = New-Object System.Threading.Mutex($false, 'Local\WeKnoraWeDriveAgentBuild')
$hasBuildMutex = $false

try {
    # PyInstaller mutates a fixed build/ and dist/ tree. Parallel invocations
    # can produce an EXE whose embedded Python archive is incomplete.
    $hasBuildMutex = $buildMutex.WaitOne(0)
    if (-not $hasBuildMutex) {
        throw 'A WeKnora Windows Agent build is already running. Wait for it to finish before starting another build.'
    }
    New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
    Push-Location $agentRoot
    try {
        uv run --python 3.11 --with pyinstaller --with . pyinstaller `
            --noconfirm `
            --clean `
            --onefile `
            --windowed `
            --name WeKnora-WeDrive-Tool `
            --collect-all playwright `
            launcher.py
        Copy-Item -Force 'dist\WeKnora-WeDrive-Tool.exe' (Join-Path $outputRoot 'WeKnora-WeDrive-Tool.exe')
    } finally {
        Pop-Location
    }
} finally {
    if ($hasBuildMutex) { [void]$buildMutex.ReleaseMutex() }
    $buildMutex.Dispose()
}

Write-Host "Windows sync tool package: $(Join-Path $outputRoot 'WeKnora-WeDrive-Tool.exe')"
