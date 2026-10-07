param([switch]$UseApprovedDemoTLS, [switch]$CheckOnly)
$ErrorActionPreference = 'Stop'
$taskRoot = 'C:\Users\28211\.codex\worktrees\source-integration\WeKnora'
$taskRunnerRoot = 'C:\Users\28211\.codex\test-runners'
$taskBackendURL = 'http://127.0.0.1:57825'
$taskFrontendURL = 'http://127.0.0.1:57826'

function Test-TaskURL([string]$URL) {
    try { return (Invoke-WebRequest -Uri $URL -UseBasicParsing -TimeoutSec 3).StatusCode -eq 200 } catch { return $false }
}

function Assert-TaskPrivateDirectory([string]$Path) {
    $taskItem = Get-Item -LiteralPath $Path
    if ($taskItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Private directory must not be a reparse point.' }
    $taskAcl = Get-Acl -LiteralPath $Path
    $taskUserSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $taskOwnerSID = $taskAcl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if (-not $taskAcl.AreAccessRulesProtected -or $taskOwnerSID -ne $taskUserSID) { throw 'Private directory ACL or owner changed.' }
    foreach ($taskRule in $taskAcl.Access) {
        $taskSID = $taskRule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($taskRule.AccessControlType -eq 'Allow' -and $taskSID -notin @($taskUserSID, 'S-1-5-18', 'S-1-5-32-544')) { throw 'Private directory has unexpected access.' }
    }
}

if ($CheckOnly) {
    Write-Host ('Backend healthy: ' + (Test-TaskURL "$taskBackendURL/health"))
    Write-Host ('Frontend healthy: ' + (Test-TaskURL $taskFrontendURL))
    exit 0
}

if (-not (Test-TaskURL "$taskBackendURL/health")) {
    foreach ($taskContainer in @('weknora-source-t16-review-57822', 'weknora-source-parser-root-t22-review', 'weknora-source-t22-redis-57824')) {
        & docker start $taskContainer | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not start $taskContainer. Start Docker Desktop first." }
    }
    Assert-TaskPrivateDirectory "$taskRunnerRoot\t22-isolated-runtime-logs"
    Assert-TaskPrivateDirectory "$taskRunnerRoot\t22-isolated-files"
    . "$taskRunnerRoot\weknora-source-t16-57822-env.ps1"
    . "$taskRunnerRoot\t22-isolated-redis\env.ps1"
    $env:T22_RUNTIME_ACL_GATE = 'verified'
    $env:T22_REHEARSAL_KB_ID = '65658207-a2ec-47fb-bf0f-11e7b685369e'
    Remove-Item Env:GITLAB_TLS_INSECURE_ORIGIN -ErrorAction SilentlyContinue
    Remove-Item Env:GITLAB_TLS_INSECURE_UNTIL -ErrorAction SilentlyContinue
    if ($UseApprovedDemoTLS) {
        $taskTLSDeadline = [DateTimeOffset]::Parse('2026-10-08T10:00:00Z')
        if ([DateTimeOffset]::UtcNow -ge $taskTLSDeadline) { throw 'Approved TLS window expired. Start without this switch to use existing Wiki/RAG with normal TLS.' }
        $env:GITLAB_TLS_INSECURE_ORIGIN = 'https://gitlab.p.it'
        $env:GITLAB_TLS_INSECURE_UNTIL = '2026-10-08T10:00:00Z'
    }
    & "$taskRunnerRoot\codewiki-basic-launch-demo-20261007.exe" --start
    if ($LASTEXITCODE -ne 0) { throw 'Backend preflight refused startup. All sources and jobs must be paused/idle; see delivery report.' }
    for ($taskAttempt = 0; $taskAttempt -lt 30; $taskAttempt++) {
        if (Test-TaskURL "$taskBackendURL/health") { break }
        Start-Sleep -Seconds 1
    }
    if (-not (Test-TaskURL "$taskBackendURL/health")) { throw 'Backend did not become healthy.' }
}

Write-Host "CodeWiki MVP: $taskFrontendURL"
Write-Host 'Existing Wiki/RAG works independently of the GitLab TLS exception.'
if (Test-TaskURL $taskFrontendURL) { Write-Host 'Frontend already running. Keep its existing terminal open.'; exit 0 }
Set-Location -LiteralPath "$taskRoot\frontend"
$env:VITE_DEV_PROXY_TARGET = $taskBackendURL
Write-Host 'Keep this window open while using CodeWiki.'
& 'C:\Program Files\nodejs\node.exe' 'node_modules/vite/bin/vite.js' --host 127.0.0.1 --port 57826 --strictPort
exit $LASTEXITCODE
