param(
    [string[]]$TestName = @(),
    [switch]$UnitTestsOnly,
    [switch]$FaultsOnly
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$parserRoot = (Resolve-Path (Join-Path $repoRoot 'sourceparser')).Path
$testsRoot = (Resolve-Path (Join-Path $parserRoot 'tests')).Path
$runId = [Guid]::NewGuid().ToString('N').Substring(0, 12)
$resourcePrefix = 'weknora-source-parser-t21-' + $runId
$imageTag = $resourcePrefix + ':offline-fault'
$containerName = $resourcePrefix + '-fixture'
$containerCreated = $false
$imageBuilt = $false

try {
    & docker version --format '{{.Server.Version}}' | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw 'Docker engine is unavailable; the isolated parser fixture was not run.'
    }

    Write-Host "Building isolated parser image $imageTag (build may use the configured package mirrors)."
    & docker build --tag $imageTag --file (Join-Path $parserRoot 'Dockerfile') $parserRoot
    if ($LASTEXITCODE -ne 0) {
        throw 'The isolated source-parser Docker build failed.'
    }
    $imageBuilt = $true

    $imageUser = (& docker image inspect --format '{{.Config.User}}' $imageTag).Trim()
    if ($LASTEXITCODE -ne 0 -or $imageUser -ne '65532:65532') {
        throw 'The parser image did not retain the expected non-root runtime user.'
    }

    $mountSource = $testsRoot.Replace('\', '/')
    $mount = 'type=bind,source=' + $mountSource + ',target=/opt/source-parser/tests,readonly'
    Write-Host 'Running real HTTP and fault checks in a networkless, resource-limited, throwaway container.'
    $containerCreated = $true
    $runArguments = @('run', '--rm', '--init', '--name', $containerName, '--pull', 'never',
        '--network', 'none', '--read-only', '--cap-drop', 'ALL',
        '--security-opt', 'no-new-privileges', '--memory', '768m', '--memory-swap', '768m',
        '--cpus', '2', '--pids-limit', '64', '--tmpfs', '/tmp:rw,noexec,nosuid,size=32m,mode=1777',
        '--tmpfs', '/contract-cache:rw,exec,nosuid,size=64m,uid=65532,gid=65532,mode=700',
        '--mount', $mount, '--workdir', '/opt/source-parser',
        '--env', 'SOURCE_PARSER_CACHE=/opt/source-parser/grammar',
        '--env', 'TMPDIR=/contract-cache')
    if ($TestName.Count -gt 0) {
        $runArguments += @('--env', ('SOURCE_PARSER_TEST_NAMES=' + ($TestName -join ';')))
    }
    if ($UnitTestsOnly) {
        $runArguments += @('--env', 'SOURCE_PARSER_FAULTS_DISABLED=1')
    }
    if ($FaultsOnly) {
        $runArguments += @('--env', 'SOURCE_PARSER_TESTS_DISABLED=1')
    }
    $runArguments += @($imageTag, 'python', '/opt/source-parser/tests/docker_fault_fixture.py')
    & docker @runArguments
    if ($LASTEXITCODE -ne 0) {
        throw 'The isolated parser HTTP/fault/offline fixture failed; see its output above.'
    }
}
finally {
    if ($containerCreated) {
        & docker rm --force $containerName 2>$null | Out-Null
    }
    if ($imageBuilt) {
        & docker image rm $imageTag 2>$null | Out-Null
    }
}
