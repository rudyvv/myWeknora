[CmdletBinding()]
param(
    [string]$CertificateThumbprint,
    [uri]$TimestampServer,
    [switch]$AllowUnsigned,
    [switch]$PublishUnsigned
)

$ErrorActionPreference = 'Stop'
$agentRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path (Join-Path $agentRoot '..\..')).Path
$outputRoot = Join-Path $repoRoot 'frontend\public\downloads'
$env:UV_CACHE_DIR = Join-Path $repoRoot '.uv-cache'

# Signing is the default. Internal unsigned distribution requires a separate
# explicit opt-in; -AllowUnsigned alone leaves the package in dist/.
$certificate = $null
if ($PublishUnsigned -and -not $AllowUnsigned) { throw '-PublishUnsigned requires -AllowUnsigned.' }
if ($AllowUnsigned) {
    if ($CertificateThumbprint -or $TimestampServer) { throw 'Do not combine signing options with -AllowUnsigned.' }
    Write-Warning 'Building an unsigned package. Publisher identity is not verified by Windows.'
} else {
    if (-not $CertificateThumbprint -or -not $TimestampServer -or $TimestampServer.Scheme -notin @('http', 'https')) {
        throw 'Release builds require -CertificateThumbprint and -TimestampServer. Use -AllowUnsigned only for local development.'
    }
    $certificate = Get-ChildItem Cert:\CurrentUser\My, Cert:\LocalMachine\My -CodeSigningCert |
        Where-Object { $_.Thumbprint -eq $CertificateThumbprint -and $_.HasPrivateKey -and $_.NotBefore -le (Get-Date) -and $_.NotAfter -gt (Get-Date) } |
        Select-Object -First 1
    if (-not $certificate) { throw 'A valid code signing certificate with a private key was not found.' }
}

$buildMutex = New-Object System.Threading.Mutex($false, 'Local\WeKnoraWeDriveAgentBuild')
$hasBuildMutex = $false
try {
    # PyInstaller mutates a fixed build/ and dist/ tree. Concurrent builds can
    # produce an EXE with an incomplete embedded Python archive.
    $hasBuildMutex = $buildMutex.WaitOne(0)
    if (-not $hasBuildMutex) { throw 'A WeKnora Windows tool build is already running.' }
    Push-Location $agentRoot
    try {
        $version = & uv run --locked --python 3.11 --with . python -c "import tomllib; from pathlib import Path; from weknora_wedrive_agent import __version__; assert tomllib.loads(Path('pyproject.toml').read_text())['project']['version'] == __version__; print(__version__)"
        if ($LASTEXITCODE -ne 0) { throw 'Unable to resolve matching project and tool versions.' }
        $version = "$version".Trim()
        if ($version -notmatch '^\d+\.\d+\.\d+$') { throw 'The tool version must be a stable major.minor.patch release.' }
        $fileVersion = "$version.0"
        $versionTuple = $fileVersion.Replace('.', ', ')
        New-Item -ItemType Directory -Force -Path 'build' | Out-Null
        $resourcePath = Join-Path $agentRoot 'build\version-info.txt'
        @"
VSVersionInfo(
  ffi=FixedFileInfo(filevers=($versionTuple), prodvers=($versionTuple), mask=0x3f,
    flags=0x0, OS=0x40004, fileType=0x1, subtype=0x0, date=(0, 0)),
  kids=[StringFileInfo([StringTable('040904B0', [
    StringStruct('CompanyName', 'WeKnora'),
    StringStruct('FileDescription', 'WeKnora WeDrive Sync Tool'),
    StringStruct('FileVersion', '$fileVersion'),
    StringStruct('ProductName', 'WeKnora WeDrive Sync Tool'),
    StringStruct('ProductVersion', '$version'),
    StringStruct('OriginalFilename', 'WeKnora-WeDrive-Tool-$version.exe')
  ])]), VarFileInfo([VarStruct('Translation', [1033, 1200])])]
)
"@ | Set-Content -LiteralPath $resourcePath -Encoding utf8
        & uv run --locked --python 3.11 --with pyinstaller==6.22.3 --with . python -m PyInstaller `
            --noconfirm --clean --onefile --windowed --name WeKnora-WeDrive-Tool `
            --version-file $resourcePath --collect-all playwright launcher.py
        if ($LASTEXITCODE -ne 0) { throw 'PyInstaller failed; the download package was not replaced.' }
        $artifact = Join-Path $agentRoot 'dist\WeKnora-WeDrive-Tool.exe'
        if ((Get-Item -LiteralPath $artifact).VersionInfo.ProductVersion -ne $version) { throw 'The executable version does not match the tool source.' }
        if (-not $AllowUnsigned) {
            $signature = Set-AuthenticodeSignature -LiteralPath $artifact -Certificate $certificate -HashAlgorithm SHA256 -TimestampServer $TimestampServer.AbsoluteUri
            if ($signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate) { throw 'Authenticode signing or timestamp verification failed.' }
        }
        $smoke = Start-Process -FilePath $artifact -ArgumentList '--version' -WindowStyle Hidden -PassThru
        if (-not $smoke.WaitForExit(30000)) {
            Stop-Process -Id $smoke.Id -Force
            throw 'The packaged tool did not exit from --version within 30 seconds.'
        }
        if ($smoke.ExitCode -ne 0) { throw 'The packaged tool failed its --version startup check.' }
        # Keep the legacy alias for configured download URLs. Both targets are
        # replaced only after build, signing and startup checks succeed.
        if (-not $AllowUnsigned -or $PublishUnsigned) {
            New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
            foreach ($name in @("WeKnora-WeDrive-Tool-$version.exe", 'WeKnora-WeDrive-Tool.exe')) {
                $target = Join-Path $outputRoot $name
                Copy-Item -LiteralPath $artifact -Destination "$target.pending" -Force
                Move-Item -LiteralPath "$target.pending" -Destination $target -Force
            }
        }
        $hash = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
        Write-Host "Windows tool $version ($((Get-Item $artifact).Length) bytes): $artifact"
        Write-Host "SHA256: $hash"
        Write-Host "Signature: $((Get-AuthenticodeSignature -LiteralPath $artifact).Status)"
    } finally { Pop-Location }
} finally {
    if ($hasBuildMutex) { [void]$buildMutex.ReleaseMutex() }
    $buildMutex.Dispose()
}
