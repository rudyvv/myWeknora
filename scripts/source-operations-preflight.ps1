[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$GitLabProbeUrl,

    [Parameter(Mandatory = $true)]
    [string]$ModelProbeUrl,

    [Parameter(Mandatory = $true)]
    [string]$ModelIdentifier,

    [Parameter(Mandatory = $true)]
    [ValidateSet('cl100k_base', 'o200k_base', 'p50k_base', 'p50k_edit', 'r50k_base')]
    [string]$Tokenizer,

    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 1000000)]
    [int]$ConfiguredInputTokenLimit,

    [Parameter(Mandatory = $true)]
    [ValidateRange(1, 1000000)]
    [int]$ProviderDocumentedHardLimit,

    [Parameter(Mandatory = $true)]
    [string]$ProviderLimitReference,

    [string[]]$ComposeFiles = @('docker-compose.yml', 'docker-compose.source.yml')
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if ([string]::IsNullOrWhiteSpace($ModelIdentifier) -or
    $ModelIdentifier -match '[\r\n]' -or
    [string]::IsNullOrWhiteSpace($ProviderLimitReference) -or
    $ProviderLimitReference -match '[\r\n]') {
    throw 'Model identifier and provider-limit reference must be non-empty single-line values.'
}
if ($ConfiguredInputTokenLimit -gt $ProviderDocumentedHardLimit) {
    throw 'Configured model input limit exceeds the provider-documented hard limit.'
}

function ConvertTo-ProbeUri([string]$Value, [string]$Label) {
    $parsed = $null
    if (-not [Uri]::TryCreate($Value, [UriKind]::Absolute, [ref]$parsed) -or
        $parsed.Scheme -ne 'https' -or [string]::IsNullOrEmpty($parsed.Host) -or
        -not [string]::IsNullOrEmpty($parsed.UserInfo) -or
        -not [string]::IsNullOrEmpty($parsed.Query) -or
        -not [string]::IsNullOrEmpty($parsed.Fragment)) {
        throw "$Label probe must be an HTTPS URL without credentials, query, or fragment."
    }
    return $parsed
}

$gitLabUri = ConvertTo-ProbeUri $GitLabProbeUrl 'GitLab'
$modelUri = ConvertTo-ProbeUri $ModelProbeUrl 'Model'
$composeArgs = @('compose')
foreach ($composeFile in $ComposeFiles) {
    $resolved = Resolve-Path (Join-Path $repoRoot $composeFile)
    $composeArgs += @('-f', $resolved.Path)
}

function Invoke-AppProbe([string]$Label, [Uri]$Target) {
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    $arguments = $composeArgs + @(
        'exec', '-T', 'app', 'curl', '--connect-timeout', '5', '--max-time', '12',
        '--silent', '--show-error', '--output', '/dev/null', '--write-out', '%{http_code}',
        $Target.AbsoluteUri
    )
    $statusText = & docker @arguments 2>$null
    $exitCode = $LASTEXITCODE
    $timer.Stop()
    if ($exitCode -ne 0) {
        throw "$Label probe failed DNS, TCP, TLS validation, or timed out (host $($Target.DnsSafeHost))."
    }
    $statusCode = 0
    if (-not [int]::TryParse(($statusText | Out-String).Trim(), [ref]$statusCode) -or
        -not (($statusCode -ge 200 -and $statusCode -lt 400) -or $statusCode -in @(401, 403))) {
        throw "$Label probe returned an unexpected HTTP status (host $($Target.DnsSafeHost))."
    }
    Write-Output ("{0}: host={1}; https_certificate=validated; http_status={2}; elapsed_ms={3}" -f `
        $Label, $Target.DnsSafeHost, $statusCode, $timer.ElapsedMilliseconds)
}

& docker version --format '{{.Server.Version}}' 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw 'Docker engine is unavailable; source deployment preflight cannot run.'
}

$healthArguments = $composeArgs + @(
    'exec', '-T', 'app', 'curl', '--fail', '--connect-timeout', '5', '--max-time', '10',
    '--silent', '--show-error', 'http://source-parser:8081/health'
)
$healthText = & docker @healthArguments 2>$null
if ($LASTEXITCODE -ne 0) {
    throw 'The app could not reach a ready source parser on its private Compose network.'
}
try {
    $health = ($healthText | Out-String) | ConvertFrom-Json
} catch {
    throw 'The source parser health response was not valid JSON.'
}
if (-not $health.ready -or -not $health.parser_version) {
    throw 'The source parser is reachable but does not advertise a ready locked runtime.'
}

Invoke-AppProbe 'GitLab' $gitLabUri
Invoke-AppProbe 'Model endpoint' $modelUri
Write-Output ("Parser: ready; version={0}; languages={1}; limits={2}" -f `
    $health.parser_version, ($health.languages -join ','),
    (($health.limits | ConvertTo-Json -Compress)))
Write-Output ("Model profile: id={0}; tokenizer={1}; configured_input_tokens={2}; provider_hard_limit={3}; provider_limit_reference_supplied=true" -f `
    $ModelIdentifier, $Tokenizer, $ConfiguredInputTokenLimit, $ProviderDocumentedHardLimit)
Write-Output 'Credentials and an actual representative model call must still be verified through the normal admin/model validation path.'
