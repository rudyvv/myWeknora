[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $BaseUrl,
    [Parameter(Mandatory = $true)] [string] $KnowledgeBaseId,
    [string] $DataSourceId,
    [Parameter(Mandatory = $true)] [string] $QuestionsFile,
    [Parameter(Mandatory = $true)] [string] $ReportPath,
    [Parameter(Mandatory = $true)] [string] $ModelIdentifier,
    [Parameter(Mandatory = $true)]
    [ValidateSet('cl100k_base', 'o200k_base', 'p50k_base', 'p50k_edit', 'r50k_base')]
    [string] $Tokenizer,
    [Parameter(Mandatory = $true)] [ValidateRange(1, 1000000)] [int] $ConfiguredInputTokenLimit,
    [Parameter(Mandatory = $true)] [ValidateRange(1, 1000000)] [int] $ProviderDocumentedHardLimit,
    [Parameter(Mandatory = $true)] [string] $ProviderLimitReference,
    [ValidateSet('sha256_utf8_lf', 'sha256_raw_utf8')]
    [string] $HashMode,
    [string] $HardwareDescription = 'unknown',
    [string] $MeasurementsFile,
    [string] $RepositorySourceMap,
    [string] $ApprovalMarkerFile,
    [string] $AgentSessionId,
    [string] $AgentId,
    [string] $AgentQuestion,
    [ValidateRange(1, 600)] [int] $RequestTimeoutSeconds = 30,
    [ValidateRange(1, 86400)] [int] $SyncTimeoutSeconds = 1800,
    [ValidateRange(1, 60)] [int] $PollIntervalSeconds = 2,
    [ValidateRange(1, 360)] [int] $RunTimeoutMinutes = 120,
    [switch] $AllowDraftQuestions,
    [switch] $Publish
)

$ErrorActionPreference = 'Stop'
$script:requestHeaders = $null
$script:baseUri = $null
$script:httpClient = $null
$script:accessToken = $null
$script:runStopwatch = $null
$script:validatedSourceMetadataCache = [System.Collections.Generic.Dictionary[string, object]]::new([StringComparer]::Ordinal)
$script:validatedSourceMetadataCacheBytes = 0
$script:authorizedSourceReadCount = 0
$script:measurementScalarFields = @('selected_files', 'selected_bytes', 'chunk_count', 'elapsed_ms', 'peak_memory_bytes', 'estimated_input_tokens', 'actual_input_tokens', 'embedding_calls', 'generation_calls')
$script:fullRunMeasurement = $null
function New-UnknownMetricStatus {
    return [ordered]@{
        selected_files = 'unknown'
        selected_bytes = 'unknown'
        chunk_count = 'unknown'
        elapsed_ms = 'unknown'
        peak_memory_bytes = 'unknown'
        estimated_input_tokens = 'unknown'
        actual_input_tokens = 'unknown'
        embedding_calls = 'unknown'
        generation_calls = 'unknown'
        phase_duration_ms = [ordered]@{ fetching = 'unknown'; parsing = 'unknown'; indexing = 'unknown'; publishing = 'unknown' }
    }
}
$script:report = [ordered]@{
    schema_version = 1
    status = 'running'
    t22_acceptance_status = 'unknown'
    started_at_utc = [DateTime]::UtcNow.ToString('o')
    scope = [ordered]@{
        knowledge_base_id = $KnowledgeBaseId
        data_source_id = $DataSourceId
        data_source_ids = @()
        snapshot_id = $null
        commit_sha = $null
    }
    model = [ordered]@{
        identifier = $ModelIdentifier
        tokenizer = $Tokenizer
        configured_input_token_limit = $ConfiguredInputTokenLimit
        provider_documented_hard_limit = $ProviderDocumentedHardLimit
        provider_limit_reference = $null
        hardware = $HardwareDescription
        evidence_hash_mode = $HashMode
    }
    parser = [ordered]@{ ready = $false; parser_version = $null; processing_version = $null }
    preview = $null
    publish = [ordered]@{ requested = [bool]$Publish; status = 'not_requested'; sync_log_id = $null; snapshot_id = $null; commit_sha = $null; telemetry = $null }
    evidence_threshold = [ordered]@{ total_questions = 30; required_top10_matches = 27; matched_count = 0; unknown_count = 0; threshold_met = $false; claim = 'top-10 evidence threshold; not full Recall' }
    source_read_validation = [ordered]@{ authorized_read_count = 0; metadata_cache_entries = 0; metadata_cache_bytes = 0; max_cache_entries = 300; max_cache_bytes = 524288 }
    question_results = @()
    incremental_runs = @(
        [ordered]@{ changed_file_count = 1; status = 'unknown'; completeness = 'unknown'; metric_status = (New-UnknownMetricStatus); input = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }; output = [ordered]@{ source_id = $null; snapshot_id = $null; commit_sha = $null; selected_files = $null; selected_bytes = $null; chunk_count = $null; phase_duration_ms = $null; elapsed_ms = $null; peak_memory_bytes = $null; estimated_input_tokens = $null; actual_input_tokens = $null; embedding_calls = $null; generation_calls = $null } },
        [ordered]@{ changed_file_count = 10; status = 'unknown'; completeness = 'unknown'; metric_status = (New-UnknownMetricStatus); input = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }; output = [ordered]@{ source_id = $null; snapshot_id = $null; commit_sha = $null; selected_files = $null; selected_bytes = $null; chunk_count = $null; phase_duration_ms = $null; elapsed_ms = $null; peak_memory_bytes = $null; estimated_input_tokens = $null; actual_input_tokens = $null; embedding_calls = $null; generation_calls = $null } },
        [ordered]@{ changed_file_count = 100; status = 'unknown'; completeness = 'unknown'; metric_status = (New-UnknownMetricStatus); input = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }; output = [ordered]@{ source_id = $null; snapshot_id = $null; commit_sha = $null; selected_files = $null; selected_bytes = $null; chunk_count = $null; phase_duration_ms = $null; elapsed_ms = $null; peak_memory_bytes = $null; estimated_input_tokens = $null; actual_input_tokens = $null; embedding_calls = $null; generation_calls = $null } }
    )
    text_baseline = [ordered]@{ status = 'unknown'; completeness = 'unknown'; metric_status = (New-UnknownMetricStatus); inputs = [ordered]@{ tokenizer = $Tokenizer; model_identifier = $ModelIdentifier; same_budget_input_token_limit = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }; outputs = [ordered]@{ selected_files = $null; selected_bytes = $null; chunk_count = $null; phase_duration_ms = $null; elapsed_ms = $null; peak_memory_bytes = $null; estimated_input_tokens = $null; actual_input_tokens = $null; embedding_calls = $null; generation_calls = $null } }
    full_run = [ordered]@{ status = 'unknown'; completeness = 'unknown'; provided_status = 'unknown'; metric_status = (New-UnknownMetricStatus); input = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }; output = [ordered]@{ source_id = $null; snapshot_id = $null; commit_sha = $null; selected_files = $null; selected_bytes = $null; chunk_count = $null; phase_duration_ms = $null; elapsed_ms = $null; peak_memory_bytes = $null; estimated_input_tokens = $null; actual_input_tokens = $null; embedding_calls = $null; generation_calls = $null } }
    agent_ui_smoke = [ordered]@{ status = 'not_requested'; event_count = 0; event_types = @(); query_sha256 = $null; answer_text_recorded = $false }
    errors = @()
}

function Get-Field($Value, [string] $Name) {
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        if ($Value.Contains($Name)) { return $Value[$Name] }
        return $null
    }
    $property = $Value.PSObject.Properties[$Name]
    if ($null -ne $property) { return $property.Value }
    return $null
}

function Get-PathSegment([string] $Value) {
    if ([string]::IsNullOrWhiteSpace($Value) -or $Value.Length -gt 128 -or $Value -match '[\r\n\x00]') {
        Stop-Acceptance 'scope_identifier_invalid' 'A scope identifier is empty or exceeds its safe path limit.'
    }
    return [Uri]::EscapeDataString($Value)
}

function Get-NormalizedFileHash([string] $Content, [string] $Mode) {
    $hashContent = $Content
    if ($Mode -eq 'sha256_utf8_lf') { $hashContent = [regex]::Replace($hashContent, "\r\n?", "`n") }
    $bytes = [System.Text.UTF8Encoding]::new($false, $true).GetBytes($hashContent)
    return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Get-SourceLineCount([string] $Content) {
    if ([string]::IsNullOrEmpty($Content)) { return 0L }
    $lineCount = [long][regex]::Matches($Content, "\r\n|\n|\r").Count
    $lastCharacter = $Content[$Content.Length - 1]
    if ($lastCharacter -ne "`r" -and $lastCharacter -ne "`n") { $lineCount++ }
    return $lineCount
}

function Add-ValidatedSourceMetadata($Metadata) {
    $identity = [ordered]@{
        knowledge_id = $Metadata.knowledge_id
        source_id = $Metadata.source_id
        snapshot_id = $Metadata.snapshot_id
        file_version_id = $Metadata.file_version_id
        commit_sha = $Metadata.commit_sha
        path = $Metadata.path
    }
    $identityJson = $identity | ConvertTo-Json -Compress -Depth 5
    $cacheKey = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($identityJson))).ToLowerInvariant()
    $cacheEntry = [ordered]@{
        knowledge_id = $Metadata.knowledge_id
        source_id = $Metadata.source_id
        snapshot_id = $Metadata.snapshot_id
        file_version_id = $Metadata.file_version_id
        commit_sha = $Metadata.commit_sha
        path = $Metadata.path
        source_sha256 = $Metadata.source_sha256
        line_count = $Metadata.line_count
    }
    $entryBytes = [System.Text.UTF8Encoding]::new($false).GetByteCount(($cacheEntry | ConvertTo-Json -Compress -Depth 5))
    if ($script:validatedSourceMetadataCache.ContainsKey($cacheKey)) {
        $existing = $script:validatedSourceMetadataCache[$cacheKey]
        if ((Get-Field $existing 'source_sha256') -cne $Metadata.source_sha256 -or (Get-Field $existing 'line_count') -ne $Metadata.line_count) {
            Stop-Acceptance 'source_identity_changed' 'The same immutable source-file identity produced different raw content metadata during this run.'
        }
        return $existing
    }
    if ($script:validatedSourceMetadataCache.Count -ge 300 -or $script:validatedSourceMetadataCacheBytes + $entryBytes -gt 524288) {
        Stop-Acceptance 'source_metadata_cache_limit' 'Verified source metadata exceeded the 300-entry or 512 KiB in-memory cache limit.'
    }
    $script:validatedSourceMetadataCache.Add($cacheKey, $cacheEntry)
    $script:validatedSourceMetadataCacheBytes += $entryBytes
    $script:report.source_read_validation.metadata_cache_entries = $script:validatedSourceMetadataCache.Count
    $script:report.source_read_validation.metadata_cache_bytes = $script:validatedSourceMetadataCacheBytes
    return $cacheEntry
}

function Get-RepositoryMapping([string] $RepositoryId) {
    if (-not $script:repositoryMappings.ContainsKey($RepositoryId)) { return $null }
    return $script:repositoryMappings[$RepositoryId]
}

function Get-SourceIdForEvidence($Evidence) {
    $repositoryId = [string](Get-Field $Evidence 'repository')
    $sourceId = [string](Get-Field $Evidence 'source_id')
    if ($repositoryId) {
        $mapping = Get-RepositoryMapping $repositoryId
        if ($null -eq $mapping) { return $null }
        if ($sourceId -and $sourceId -ne (Get-Field $mapping 'source_id')) { return $null }
        return [string](Get-Field $mapping 'source_id')
    }
    if ($sourceId) { return $sourceId }
    return $DataSourceId
}

function Stop-Acceptance([string] $Code, [string] $Message) {
    throw "ACCEPTANCE_FAILURE|$Code|$Message"
}

function Convert-OptionalNonNegativeInteger($Value, [string] $Label, [string] $ErrorCode = 'measurement_value_invalid') {
    if ($null -eq $Value) { return $null }
    $typeName = $Value.GetType().FullName
    $integerTypes = @('System.Byte', 'System.SByte', 'System.Int16', 'System.UInt16', 'System.Int32', 'System.UInt32', 'System.Int64', 'System.UInt64')
    if ($typeName -notin $integerTypes) {
        Stop-Acceptance $ErrorCode "Measurement '$Label' must be a non-negative JSON integer or null."
    }
    $number = [decimal]$Value
    if ($number -lt 0 -or $number -gt [decimal][long]::MaxValue -or $number -ne [decimal]::Truncate($number)) {
        Stop-Acceptance $ErrorCode "Measurement '$Label' must be a non-negative JSON integer or null."
    }
    return [long]$number
}

function Get-ValidatedMeasurementMetrics($Record, [string] $Label, [string] $ErrorCode = 'measurement_value_invalid') {
    $scalars = [ordered]@{}
    foreach ($field in $script:measurementScalarFields) {
        $scalars[$field] = Convert-OptionalNonNegativeInteger (Get-Field $Record $field) "$Label.$field" $ErrorCode
    }
    $phaseInput = Get-Field $Record 'phase_duration_ms'
    $phaseOutput = $null
    if ($null -ne $phaseInput) {
        if ($phaseInput -isnot [System.Management.Automation.PSCustomObject] -and $phaseInput -isnot [System.Collections.IDictionary]) {
            Stop-Acceptance $ErrorCode "Measurement '$Label.phase_duration_ms' must be an object or null."
        }
        $phaseOutput = [ordered]@{ fetching = $null; parsing = $null; indexing = $null; publishing = $null }
        if ($phaseInput -is [System.Collections.IDictionary]) { $phaseNames = @($phaseInput.Keys) } else { $phaseNames = @($phaseInput.PSObject.Properties | ForEach-Object { $_.Name }) }
        foreach ($phaseName in $phaseNames) {
            if ($phaseName -notin @('fetching', 'parsing', 'indexing', 'publishing')) {
                Stop-Acceptance $ErrorCode "Measurement '$Label.phase_duration_ms' contains an unsupported phase."
            }
            $phaseOutput[$phaseName] = Convert-OptionalNonNegativeInteger (Get-Field $phaseInput $phaseName) "$Label.phase_duration_ms.$phaseName" $ErrorCode
        }
    }
    return [pscustomobject]@{ scalar = $scalars; phase_duration_ms = $phaseOutput }
}

function Test-CanonicalGitRelativePath($Path) {
    if ($Path -isnot [string] -or [string]::IsNullOrEmpty($Path) -or $Path.Length -gt 1024 -or $Path.StartsWith('/') -or $Path.StartsWith('\') -or $Path.Contains('\') -or $Path -match '^[A-Za-z]:') { return $false }
    foreach ($character in $Path.ToCharArray()) {
        if ([char]::IsControl($character)) { return $false }
    }
    foreach ($segment in $Path.Split([char]'/')) {
        if ([string]::IsNullOrEmpty($segment) -or $segment -eq '.' -or $segment -eq '..') { return $false }
    }
    return $true
}

function Convert-OptionalFiniteJsonNumber($Value, [string] $Label) {
    if ($null -eq $Value) { return $null }
    if ($Value -is [bool] -or $Value -is [string] -or $Value -is [char]) {
        Stop-Acceptance 'search_hit_score_invalid' "Search hit '$Label' must be a finite JSON number or null."
    }
    $typeName = $Value.GetType().FullName
    $integerTypes = @('System.Byte', 'System.SByte', 'System.Int16', 'System.UInt16', 'System.Int32', 'System.UInt32', 'System.Int64', 'System.UInt64')
    if ($typeName -notin $integerTypes -and $Value -isnot [decimal] -and $Value -isnot [double] -and $Value -isnot [single]) {
        Stop-Acceptance 'search_hit_score_invalid' "Search hit '$Label' must be a finite JSON number or null."
    }
    try { $number = [double]$Value } catch {
        Stop-Acceptance 'search_hit_score_invalid' "Search hit '$Label' must be a finite JSON number or null."
    }
    if ([double]::IsNaN($number) -or [double]::IsInfinity($number)) {
        Stop-Acceptance 'search_hit_score_invalid' "Search hit '$Label' must be a finite JSON number or null."
    }
    return $number
}

function Convert-SearchMatchType($Value) {
    if ($null -eq $Value) { Stop-Acceptance 'search_hit_match_type_invalid' 'Search hit match type is not a recognized numeric match enum.' }
    $matchType = Convert-OptionalNonNegativeInteger $Value 'search_hit.match_type' 'search_hit_match_type_invalid'
    if ($matchType -gt 9) { Stop-Acceptance 'search_hit_match_type_invalid' 'Search hit match type is not a recognized numeric match enum.' }
    return $matchType
}

function Assert-AgentResponseType([string] $Value) {
    $knownTypes = @('answer', 'references', 'thinking', 'tool_call', 'tool_result', 'install_output', 'command_output', 'error', 'reflection', 'session_title', 'agent_query', 'complete', 'artifacts_pending', 'tool_approval_required', 'tool_approval_resolved', 'mcp_oauth_required', 'mcp_oauth_resolved', 'memory_recalled', 'steer', 'user_message_injected', 'context_compacted', 'install_prompt')
    if ([string]::IsNullOrWhiteSpace($Value) -or $Value -cnotin $knownTypes) {
        Stop-Acceptance 'agent_ui_event_invalid' 'The UI agent stream contained an unknown response type; event content was suppressed.'
    }
}

function Set-FullRunMeasurement($Record) {
    if ($null -eq $Record) { return }
    if ((Get-Field $Record 'model_identifier') -cne $ModelIdentifier -or (Get-Field $Record 'tokenizer') -cne $Tokenizer -or (Get-Field $Record 'context_limit_tokens') -ne $ConfiguredInputTokenLimit) {
        Stop-Acceptance 'full_run_budget_mismatch' 'Full-run measurement must use the declared model, tokenizer, and input-token budget.'
    }
    $providedStatus = [string](Get-Field $Record 'status')
    if ($providedStatus -notin @('measured', 'unknown')) { Stop-Acceptance 'full_run_metrics_invalid' 'Full-run measurement status must be measured or unknown.' }
    $metrics = Get-ValidatedMeasurementMetrics $Record 'full_run' 'full_run_metrics_invalid'
    $summary = Get-MeasurementSummary $Record
    $sourceIdValue = Get-Field $Record 'source_id'
    $snapshotIdValue = Get-Field $Record 'snapshot_id'
    $commitShaValue = Get-Field $Record 'commit_sha'
    foreach ($identityValue in @($sourceIdValue, $snapshotIdValue, $commitShaValue)) {
        if ($null -ne $identityValue -and $identityValue -isnot [string]) {
            Stop-Acceptance 'full_run_scope_mismatch' 'Full-run source identity fields must be strings or null.'
        }
    }
    $sourceId = if ($null -ne $sourceIdValue) { $sourceIdValue } else { '' }
    $snapshotId = if ($null -ne $snapshotIdValue) { $snapshotIdValue } else { '' }
    $commitSha = if ($null -ne $commitShaValue) { $commitShaValue } else { '' }
    $hasIdentity = -not [string]::IsNullOrWhiteSpace($sourceId) -or -not [string]::IsNullOrWhiteSpace($snapshotId) -or -not [string]::IsNullOrWhiteSpace($commitSha)
    if ($hasIdentity -or $summary.completeness -ne 'unknown' -or $providedStatus -eq 'measured') {
        if ([string]::IsNullOrWhiteSpace($sourceId) -or [string]::IsNullOrWhiteSpace($snapshotId) -or [string]::IsNullOrWhiteSpace($commitSha) -or
            $script:dataSourceIds -cnotcontains $sourceId -or -not $script:publishedBySource.ContainsKey($sourceId)) {
            Stop-Acceptance 'full_run_scope_mismatch' 'Full-run measurement does not identify one selected published source snapshot.'
        }
        $publishedSnapshot = $script:publishedBySource[$sourceId].snapshot
        if ($snapshotId -cne [string](Get-Field $publishedSnapshot 'id') -or $commitSha -cne [string](Get-Field $publishedSnapshot 'commit_sha')) {
            Stop-Acceptance 'full_run_scope_mismatch' 'Full-run measurement does not match the selected published source snapshot and commit.'
        }
    }
    $script:report.full_run = [ordered]@{
        status = $summary.status
        completeness = $summary.completeness
        provided_status = $providedStatus
        metric_status = $summary.metric_status
        input = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }
        output = [ordered]@{
            source_id = if ($hasIdentity) { $sourceId } else { $null }
            snapshot_id = if ($hasIdentity) { $snapshotId } else { $null }
            commit_sha = if ($hasIdentity) { $commitSha } else { $null }
            selected_files = $metrics.scalar.selected_files
            selected_bytes = $metrics.scalar.selected_bytes
            chunk_count = $metrics.scalar.chunk_count
            phase_duration_ms = $metrics.phase_duration_ms
            elapsed_ms = $metrics.scalar.elapsed_ms
            peak_memory_bytes = $metrics.scalar.peak_memory_bytes
            estimated_input_tokens = $metrics.scalar.estimated_input_tokens
            actual_input_tokens = $metrics.scalar.actual_input_tokens
            embedding_calls = $metrics.scalar.embedding_calls
            generation_calls = $metrics.scalar.generation_calls
        }
    }
}

function Get-MeasurementSummary($Record) {
    $metricStatus = [ordered]@{}
    $observedCount = 0
    $requiredCount = $script:measurementScalarFields.Count + 4
    foreach ($field in $script:measurementScalarFields) {
        if ($null -ne (Get-Field $Record $field)) {
            $metricStatus[$field] = 'observed'
            $observedCount++
        } else {
            $metricStatus[$field] = 'unknown'
        }
    }
    $phaseStatus = [ordered]@{}
    $phases = Get-Field $Record 'phase_duration_ms'
    foreach ($phase in @('fetching', 'parsing', 'indexing', 'publishing')) {
        if ($null -ne (Get-Field $phases $phase)) {
            $phaseStatus[$phase] = 'observed'
            $observedCount++
        } else {
            $phaseStatus[$phase] = 'unknown'
        }
    }
    $metricStatus.phase_duration_ms = $phaseStatus
    $completeness = if ($observedCount -eq 0) { 'unknown' } elseif ($observedCount -eq $requiredCount) { 'complete' } else { 'partial' }
    $status = if ($completeness -eq 'complete') { 'measured' } else { $completeness }
    return [pscustomobject]@{ status = $status; completeness = $completeness; metric_status = $metricStatus }
}

function Write-Report {
    $script:report.finished_at_utc = [DateTime]::UtcNow.ToString('o')
    $parent = Split-Path -Parent $ReportPath
    if (-not [string]::IsNullOrWhiteSpace($parent) -and -not (Test-Path -LiteralPath $parent)) {
        New-Item -ItemType Directory -Path $parent -Force | Out-Null
    }
    $json = $script:report | ConvertTo-Json -Depth 80
    $encoding = [System.Text.UTF8Encoding]::new($false)
    if ($encoding.GetByteCount($json) -gt 33554432) {
        $script:report.status = 'failed'
        $script:report.preview = $null
        $script:report.publish.telemetry = $null
        $script:report.question_results = @()
        $script:report.errors = @([ordered]@{ code = 'report_size_exceeded'; message = 'The full evidence report exceeded the 32 MiB output limit and was reduced to a bounded failure summary.' })
        $script:report.evidence_threshold.matched_count = 0
        $script:report.evidence_threshold.unknown_count = 30
        $script:report.evidence_threshold.threshold_met = $false
        $json = $script:report | ConvertTo-Json -Depth 20
    }
    if ($encoding.GetByteCount($json) -gt 33554432) {
        Stop-Acceptance 'report_size_exceeded' 'The bounded failure report still exceeded the 32 MiB output limit.'
    }
    [System.IO.File]::WriteAllText([System.IO.Path]::GetFullPath($ReportPath), $json, $encoding)
}

function Invoke-AcceptanceHttp([string] $Method, [string] $Path, $Body = $null, [string] $Accept = 'application/json') {
    $uri = [Uri]::new($script:baseUri, $Path.TrimStart('/'))
    $remaining = [TimeSpan]::FromMinutes($RunTimeoutMinutes) - $script:runStopwatch.Elapsed
    if ($remaining.TotalSeconds -le 0) { Stop-Acceptance 'run_timeout' 'The acceptance run exceeded its overall deadline.' }
    $timeoutSeconds = [Math]::Max(1, [Math]::Min($RequestTimeoutSeconds, [int][Math]::Ceiling($remaining.TotalSeconds)))
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method), $uri)
    $request.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $script:accessToken)
    $request.Headers.Accept.ParseAdd($Accept)
    if ($null -ne $Body) {
        $encodedBody = [System.Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 80 -Compress))
        $request.Content = [System.Net.Http.ByteArrayContent]::new($encodedBody)
        $request.Content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new('application/json')
        $request.Content.Headers.ContentType.CharSet = 'utf-8'
    }
    $cancellation = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromSeconds($timeoutSeconds))
    $response = $null
    $stream = $null
    $memory = $null
    try {
        $response = $script:httpClient.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cancellation.Token).GetAwaiter().GetResult()
        if ($response.Content.Headers.ContentLength -gt 16777216) {
            Stop-Acceptance 'http_response_too_large' "The $Method $Path response exceeded the 16 MiB report-runner limit."
        }
        $stream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $memory = [System.IO.MemoryStream]::new()
        $buffer = [byte[]]::new(65536)
        while (($read = $stream.ReadAsync($buffer, 0, $buffer.Length, $cancellation.Token).GetAwaiter().GetResult()) -gt 0) {
            if ($memory.Length + $read -gt 16777216) {
                Stop-Acceptance 'http_response_too_large' "The $Method $Path response exceeded the 16 MiB report-runner limit."
            }
            $memory.Write($buffer, 0, $read)
        }
        $content = [System.Text.UTF8Encoding]::new($false, $true).GetString($memory.ToArray())
        return [pscustomobject]@{ StatusCode = [int]$response.StatusCode; Content = $content }
    } catch {
        if ($_.Exception.Message -match '^ACCEPTANCE_FAILURE\|') { throw }
        Stop-Acceptance 'http_transport_error' "The $Method $Path request did not complete within its bounded transport policy."
    } finally {
        if ($memory) { $memory.Dispose() }
        if ($stream) { $stream.Dispose() }
        if ($response) { $response.Dispose() }
        $request.Dispose()
        $cancellation.Dispose()
    }
}

function Invoke-AcceptanceJson([string] $Method, [string] $Path, $Body = $null) {
    $response = Invoke-AcceptanceHttp $Method $Path $Body
    if ([int]$response.StatusCode -lt 200 -or [int]$response.StatusCode -ge 300) {
        $safeStatus = [int]$response.StatusCode
        Stop-Acceptance "http_$safeStatus" "The $Method $Path request returned HTTP $safeStatus; response content was suppressed."
    }
    try {
        return $response.Content | ConvertFrom-Json -Depth 80 -NoEnumerate
    } catch {
        Stop-Acceptance 'invalid_json_response' "The $Method $Path response was not valid JSON."
    }
}

function Assert-NoSensitiveSettings($Value, [string] $Path = 'settings') {
    if ($null -eq $Value) { return }
    if ($Value -is [System.Collections.IDictionary]) {
        foreach ($key in $Value.Keys) {
            if ([string]$key -match '(?i)token|secret|password|credential|private.?key|client.?secret|api.?key|authorization|bearer|auth') {
                Stop-Acceptance 'unsafe_source_settings' "Source settings contain a sensitive-named field at $Path; request was not sent."
            }
            Assert-NoSensitiveSettings $Value[$key] "$Path.$key"
        }
        return
    }
    if ($Value -is [System.Collections.IEnumerable] -and $Value -isnot [string]) {
        foreach ($item in $Value) { Assert-NoSensitiveSettings $item $Path }
        return
    }
    if ($Value -is [pscustomobject]) {
        foreach ($property in $Value.PSObject.Properties) {
            if ($property.Name -match '(?i)token|secret|password|credential|private.?key|client.?secret|api.?key|authorization|bearer|auth') {
                Stop-Acceptance 'unsafe_source_settings' "Source settings contain a sensitive-named field at $Path; request was not sent."
            }
            Assert-NoSensitiveSettings $property.Value "$Path.$($property.Name)"
        }
    }
}

function Convert-LogResult($Log) {
    $result = Get-Field $Log 'result'
    if ($result -is [string]) {
        try { $result = $result | ConvertFrom-Json -Depth 80 -NoEnumerate } catch { return $null }
    }
    return $result
}

function Get-SafeCounter($Value) {
    if ($null -eq $Value) { return $null }
    $number = 0L
    if ([long]::TryParse([string]$Value, [ref]$number) -and $number -ge 0) { return $number }
    return $null
}

function Select-SafeCounterMap($InputMap, [string[]] $AllowedKeys) {
    if ($null -eq $InputMap) { return $null }
    $safe = [ordered]@{}
    foreach ($key in $AllowedKeys) {
        $value = Get-SafeCounter (Get-Field $InputMap $key)
        if ($null -ne $value) { $safe[$key] = $value }
    }
    if ($safe.Count -eq 0) { return $null }
    return $safe
}

function Select-SafeTelemetry($Telemetry) {
    if ($null -eq $Telemetry) { return $null }
    $safe = [ordered]@{}
    if ((Get-Field $Telemetry 'schema_version') -eq 1) { $safe.schema_version = 1 }
    foreach ($field in @('selected_bytes', 'git_transfer_bytes', 'lease_recoveries', 'cleanup_residue_count')) {
        $value = Get-SafeCounter (Get-Field $Telemetry $field)
        if ($null -ne $value) { $safe[$field] = $value }
    }
    $publishedCommit = [string](Get-Field $Telemetry 'published_commit_sha')
    if ($publishedCommit -match '^[0-9a-fA-F]{7,128}$') { $safe.published_commit_sha = $publishedCommit }
    $phaseDurations = Select-SafeCounterMap (Get-Field $Telemetry 'phase_duration_ms') @('fetching', 'parsing', 'indexing', 'publishing')
    if ($phaseDurations) { $safe.phase_duration_ms = $phaseDurations }
    $qualityCounts = Select-SafeCounterMap (Get-Field $Telemetry 'quality_counts') @('structural', 'partial', 'syntax_error', 'degraded', 'unknown_preprocess', 'text_fallback')
    if ($qualityCounts) { $safe.quality_counts = $qualityCounts }

    $storage = Get-Field $Telemetry 'storage'
    if ($null -ne $storage) {
        $safeStorage = [ordered]@{}
        foreach ($name in @('cache', 'staging', 'original', 'vectors')) {
            $metric = Get-Field $storage $name
            if ($null -eq $metric) { continue }
            $usedBytes = Get-SafeCounter (Get-Field $metric 'used_bytes')
            $limitBytes = Get-SafeCounter (Get-Field $metric 'limit_bytes')
            $measurement = [string](Get-Field $metric 'measurement')
            if ($null -ne $usedBytes -and $null -ne $limitBytes -and $limitBytes -gt 0 -and $measurement -in @('logical_payload', 'physical')) {
                $safeStorage[$name] = [ordered]@{ used_bytes = $usedBytes; limit_bytes = $limitBytes; measurement = $measurement }
            }
        }
        if ($safeStorage.Count -gt 0) { $safe.storage = $safeStorage }
    }

    $coverage = Get-Field $Telemetry 'wiki_coverage'
    if ($null -ne $coverage) {
        $safeCoverage = Select-SafeCounterMap $coverage @('eligible', 'ready', 'stale', 'failed', 'ungenerated', 'deferred')
        if ($safeCoverage -and $safeCoverage.Count -eq 6) { $safe.wiki_coverage = $safeCoverage }
    }
    $usage = Get-Field $Telemetry 'model_usage'
    if ($null -ne $usage) {
        $safeUsage = Select-SafeCounterMap $usage @('embedding_calls', 'estimated_input_tokens')
        if ($safeUsage) { $safe.model_usage = $safeUsage }
    }
    return $safe
}

function Get-PublishedRunFromLogs([string] $SourceId) {
    $pageSize = 100
    $maxPages = 100
    for ($page = 0; $page -lt $maxPages; $page++) {
        $offset = $page * $pageSize
        $logs = Invoke-AcceptanceJson 'GET' "/datasource/$(Get-PathSegment $SourceId)/logs?limit=$pageSize&offset=$offset"
        $entries = @($logs)
        foreach ($entry in $entries) {
            if ((Get-Field $entry 'status') -ne 'success') { continue }
            $result = Convert-LogResult $entry
            $sourceResult = Get-Field $result 'source'
            $snapshot = Get-Field $sourceResult 'snapshot'
            if ((Get-Field $snapshot 'state') -ne 'published') { continue }
            if ((Get-Field $snapshot 'data_source_id') -ne $SourceId -or (Get-Field $snapshot 'knowledge_base_id') -ne $KnowledgeBaseId) {
                continue
            }
            return [pscustomobject]@{ log = $entry; snapshot = $snapshot; telemetry = Select-SafeTelemetry (Get-Field $sourceResult 'telemetry') }
        }
        if ($entries.Count -lt $pageSize) { break }
    }
    Stop-Acceptance 'published_snapshot_not_found' 'No completed published snapshot was found within the bounded sync-log pages; run preview and an explicitly approved publish first.'
}

function Wait-ForPublishedRun($InitialLog, [string] $SourceId) {
    $logId = [string](Get-Field $InitialLog 'id')
    if ([string]::IsNullOrWhiteSpace($logId)) { Stop-Acceptance 'sync_log_id_missing' 'The sync endpoint did not return a sync log ID.' }
    $script:report.publish.sync_log_id = $logId
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    $log = $InitialLog
    while ((Get-Field $log 'status') -in @('queued', 'pending', 'running')) {
        if ($timer.Elapsed.TotalSeconds -ge $SyncTimeoutSeconds) {
            $script:report.publish.status = 'timeout'
            Stop-Acceptance 'sync_timeout' 'The source sync did not finish before the configured deadline.'
        }
        Start-Sleep -Seconds $PollIntervalSeconds
        $log = Invoke-AcceptanceJson 'GET' "/datasource/logs/$(Get-PathSegment $logId)"
    }
    $status = [string](Get-Field $log 'status')
    $script:report.publish.status = $status
    if ($status -ne 'success') {
        Stop-Acceptance 'sync_not_successful' "The source sync ended with status '$status'; server error details were suppressed."
    }
    $result = Convert-LogResult $log
    $sourceResult = Get-Field $result 'source'
    $snapshot = Get-Field $sourceResult 'snapshot'
    if ((Get-Field $snapshot 'state') -ne 'published' -or
        (Get-Field $snapshot 'data_source_id') -ne $SourceId -or
        (Get-Field $snapshot 'knowledge_base_id') -ne $KnowledgeBaseId) {
        Stop-Acceptance 'published_snapshot_mismatch' 'The completed sync log did not identify a published snapshot in the selected KB/source scope.'
    }
    return [pscustomobject]@{ log = $log; snapshot = $snapshot; telemetry = Select-SafeTelemetry (Get-Field $sourceResult 'telemetry') }
}

function Get-ChunkSourceRange($Hit) {
    $chunkMetadata = Get-Field $Hit 'chunk_metadata'
    if ($chunkMetadata -is [string]) {
        try { $chunkMetadata = $chunkMetadata | ConvertFrom-Json -Depth 40 -NoEnumerate } catch { return $null }
    }
    $sourceMetadata = Get-Field $chunkMetadata 'source'
    return Get-Field $sourceMetadata 'range'
}

function Read-AuthorizedSourceFile([string] $KnowledgeId, [string] $VersionId) {
    $escapedKnowledge = [Uri]::EscapeDataString($KnowledgeId)
    $escapedVersion = [Uri]::EscapeDataString($VersionId)
    $path = "/knowledge/$escapedKnowledge/source?version_id=$escapedVersion"
    $script:authorizedSourceReadCount++
    $script:report.source_read_validation.authorized_read_count = $script:authorizedSourceReadCount
    $response = Invoke-AcceptanceHttp 'GET' $path
    if ([int]$response.StatusCode -ne 200) {
        $readStatus = if ([int]$response.StatusCode -in @(401, 403)) { 'denied' } elseif ([int]$response.StatusCode -eq 404) { 'unavailable' } else { 'http_error' }
        return [pscustomobject]@{ status = $readStatus; view = $null }
    }
    try { $body = $response.Content | ConvertFrom-Json -Depth 40 -NoEnumerate } catch {
        return [pscustomobject]@{ status = 'invalid_response'; view = $null }
    }
    return [pscustomobject]@{ status = 'read'; view = (Get-Field $body 'data') }
}

function Invoke-QuestionSearch($Question) {
    $questionId = [string](Get-Field $Question 'question_id')
    $category = [string](Get-Field $Question 'category')
    $query = [string](Get-Field $Question 'query')
    $expectedItems = @(Get-Field $Question 'expected_evidence')
    $sourceIds = @($expectedItems | ForEach-Object { [string](Get-Field $_ 'source_id') } | Select-Object -Unique)
    foreach ($sourceId in $sourceIds) {
        if (-not $script:publishedBySource.ContainsKey($sourceId)) { Stop-Acceptance 'published_snapshot_not_found' "Question '$questionId' references a source with no selected published snapshot." }
    }
    $request = [ordered]@{ query_text = $query; match_count = 10; source_ids = $sourceIds }
    $search = Invoke-AcceptanceJson 'POST' "/knowledge-bases/$(Get-PathSegment $KnowledgeBaseId)/hybrid-search" $request
    $hits = @(Get-Field $search 'data')
    if ($hits.Count -gt 10) { Stop-Acceptance 'search_topk_contract_violation' "Question '$questionId' returned more than ten evidence rows." }
    $evidence = [System.Collections.Generic.List[object]]::new()
    $evidenceSatisfied = @{}
    foreach ($expectedItem in $expectedItems) { $evidenceSatisfied[[string](Get-Field $expectedItem 'evidence_id')] = $false }
    $matchedEvidenceIds = [System.Collections.Generic.List[string]]::new()
    $rank = 0
    foreach ($hit in $hits) {
        $rank++
        $metadata = Get-Field $hit 'metadata'
        $sourceId = [string](Get-Field $metadata 'datasource_id')
        $snapshotId = [string](Get-Field $metadata 'source_snapshot_id')
        $commit = [string](Get-Field $metadata 'commit_sha')
        $pathValue = Get-Field $metadata 'source_path'
        $path = if ($pathValue -is [string]) { $pathValue } else { $null }
        $versionId = [string](Get-Field $metadata 'source_file_version_id')
        $knowledgeId = [string](Get-Field $hit 'knowledge_id')
        if ($sourceId -notin $sourceIds -or -not $script:publishedBySource.ContainsKey($sourceId)) {
            Stop-Acceptance 'search_scope_violation' "Question '$questionId' returned evidence from a source outside its repository mapping."
        }
        $snapshotRecord = $script:publishedBySource[$sourceId]
        if ($snapshotId -ne (Get-Field $snapshotRecord.snapshot 'id') -or $commit -ne (Get-Field $snapshotRecord.snapshot 'commit_sha')) {
            Stop-Acceptance 'search_scope_violation' "Question '$questionId' returned evidence outside the selected published source snapshot."
        }
        if (-not $knowledgeId -or $knowledgeId.Length -gt 128 -or -not $versionId -or $versionId.Length -gt 128 -or
            -not (Test-CanonicalGitRelativePath $path)) {
            Stop-Acceptance 'search_hit_identity_invalid' "Question '$questionId' returned an incomplete or unsafe fixed-version source identity."
        }
        $range = Get-ChunkSourceRange $hit
        $start = 0L
        $end = 0L
        if ($null -eq $range -or
            -not [long]::TryParse([string](Get-Field $range 'start_line'), [ref]$start) -or
            -not [long]::TryParse([string](Get-Field $range 'end_line'), [ref]$end) -or
            $start -lt 1 -or $end -lt $start -or $end -gt 16777216) {
            Stop-Acceptance 'search_hit_range_invalid' "Question '$questionId' returned an invalid source line range."
        }
        $row = [ordered]@{
            rank = $rank
            knowledge_id = $knowledgeId
            source_id = $sourceId
            snapshot_id = $snapshotId
            commit_sha = $commit
            path = $path
            file_version_id = $versionId
            start_line = $start
            end_line = $end
            score = (Convert-OptionalFiniteJsonNumber (Get-Field $hit 'score') 'score')
            match_type = (Convert-SearchMatchType (Get-Field $hit 'match_type'))
            verified_source_sha256 = $null
            source_line_count = $null
            matched_normalized_sha256 = $null
            authorized_read_status = 'pending'
            matched_expected_evidence_ids = @()
        }
        $matchingExpected = [System.Collections.Generic.List[object]]::new()
        foreach ($expectedItem in $expectedItems) {
            $expectedSourceId = [string](Get-Field $expectedItem 'source_id')
            $expectedSnapshotId = [string](Get-Field $expectedItem 'snapshot_id')
            $expectedCommit = [string](Get-Field $expectedItem 'commit_sha')
            $expectedPath = [string](Get-Field $expectedItem 'path')
            $expectedStart = [int](Get-Field $expectedItem 'start_line')
            $expectedEnd = [int](Get-Field $expectedItem 'end_line')
            $expectedId = [string](Get-Field $expectedItem 'evidence_id')
            if ($sourceId -eq $expectedSourceId -and $path -ceq $expectedPath -and
                $null -ne $start -and $null -ne $end -and [int]$start -le $expectedStart -and [int]$end -ge $expectedEnd -and
                $snapshotId -ceq $expectedSnapshotId -and $commit -ceq $expectedCommit -and $versionId) {
                $matchingExpected.Add($expectedItem)
            }
        }

        # Every returned top-10 row must pass a normal authorized, fixed-version read,
        # including rows that cannot match any gold evidence span.
        $read = Read-AuthorizedSourceFile $knowledgeId $versionId
        if ($read.status -ne 'read') {
            Stop-Acceptance 'source_hit_read_failed' "Question '$questionId' top-10 hit failed its authorized fixed-version source read."
        }
        $view = $read.view
        $content = [string](Get-Field $view 'content')
        $actualRawHash = Get-NormalizedFileHash $content 'raw_utf8'
        $storedHash = ([string](Get-Field $view 'sha256')).ToLowerInvariant()
        if ($actualRawHash -ne $storedHash -or
            (Get-Field $view 'knowledge_id') -ne $knowledgeId -or
            (Get-Field $view 'data_source_id') -ne $sourceId -or
            (Get-Field $view 'snapshot_id') -ne $snapshotId -or
            (Get-Field $view 'file_version_id') -ne $versionId -or
            (Get-Field $view 'commit_sha') -ne $commit -or
            (Get-Field $view 'path') -cne $path) {
            Stop-Acceptance 'source_read_identity_mismatch' "Question '$questionId' source read did not match its immutable search evidence identity."
        }
        $lineCount = Get-SourceLineCount $content
        if ($end -gt $lineCount) {
            Stop-Acceptance 'search_hit_range_invalid' "Question '$questionId' returned a source line range beyond the authorized file's line count."
        }
        $verifiedMetadata = Add-ValidatedSourceMetadata ([ordered]@{
            knowledge_id = $knowledgeId
            source_id = $sourceId
            snapshot_id = $snapshotId
            file_version_id = $versionId
            commit_sha = $commit
            path = $path
            source_sha256 = $actualRawHash
            line_count = $lineCount
        })
        $row.verified_source_sha256 = Get-Field $verifiedMetadata 'source_sha256'
        $row.source_line_count = Get-Field $verifiedMetadata 'line_count'
        $row.authorized_read_status = 'read'

        $parserVersion = [string](Get-Field $view 'parser_version')
        if ($parserVersion -and -not $script:report.parser.parser_version) { $script:report.parser.parser_version = $parserVersion }
        foreach ($expectedItem in $matchingExpected) {
            $evidenceId = [string](Get-Field $expectedItem 'evidence_id')
            $normalizedHash = Get-NormalizedFileHash $content ([string](Get-Field $expectedItem 'hash_mode'))
            if ($normalizedHash -ceq [string](Get-Field $expectedItem 'sha256')) {
                $evidenceSatisfied[$evidenceId] = $true
                $matchedEvidenceIds.Add($evidenceId)
                $row.matched_normalized_sha256 = $normalizedHash
                $row.matched_expected_evidence_ids += $evidenceId
            }
        }
        $evidence.Add($row)
        $content = $null
        $view = $null
        $read = $null
        $verifiedMetadata = $null
    }
    $queryDigest = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($query))).ToLowerInvariant()
    $allExpectedFound = $true
    foreach ($expectedItem in $expectedItems) { if (-not $evidenceSatisfied[[string](Get-Field $expectedItem 'evidence_id')]) { $allExpectedFound = $false } }
    $status = if ($allExpectedFound) { 'matched' } else { 'unknown' }
    $expectedReport = @($expectedItems | ForEach-Object { [ordered]@{ evidence_id = Get-Field $_ 'evidence_id'; repository = Get-Field $_ 'repository'; source_id = Get-Field $_ 'source_id'; snapshot_id = Get-Field $_ 'snapshot_id'; commit_sha = Get-Field $_ 'commit_sha'; path = Get-Field $_ 'path'; start_line = Get-Field $_ 'start_line'; end_line = Get-Field $_ 'end_line'; sha256 = Get-Field $_ 'sha256'; hash_mode = Get-Field $_ 'hash_mode'; matched = [bool]$evidenceSatisfied[[string](Get-Field $_ 'evidence_id')] } })
    $sourceSnapshotScope = @($sourceIds | ForEach-Object { $snapshotRecord = $script:publishedBySource[$_]; [ordered]@{ source_id = $_; snapshot_id = Get-Field $snapshotRecord.snapshot 'id'; commit_sha = Get-Field $snapshotRecord.snapshot 'commit_sha' } })
    return [ordered]@{
        question_id = $questionId
        category = $category
        query_sha256 = $queryDigest
        repository_ids = @($expectedItems | ForEach-Object { Get-Field $_ 'repository' } | Select-Object -Unique)
        source_snapshot_scope = $sourceSnapshotScope
        expected_evidence = $expectedReport
        retrieved_top10 = @($evidence.ToArray())
        matched_expected_evidence_ids = @($matchedEvidenceIds.ToArray() | Select-Object -Unique)
        status = $status
    }
}

function Invoke-AgentUiSmoke {
    if ([string]::IsNullOrWhiteSpace($AgentSessionId) -and [string]::IsNullOrWhiteSpace($AgentId) -and [string]::IsNullOrWhiteSpace($AgentQuestion)) { return }
    if ([string]::IsNullOrWhiteSpace($AgentSessionId) -or [string]::IsNullOrWhiteSpace($AgentId) -or [string]::IsNullOrWhiteSpace($AgentQuestion)) {
        Stop-Acceptance 'agent_smoke_configuration_incomplete' 'Agent UI smoke requires session ID, agent ID, and one query together.'
    }
    $digest = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData([System.Text.Encoding]::UTF8.GetBytes($AgentQuestion))).ToLowerInvariant()
    $script:report.agent_ui_smoke.query_sha256 = $digest
    $script:report.agent_ui_smoke.status = 'running'
    $request = [ordered]@{
        query = $AgentQuestion
        knowledge_base_ids = @($KnowledgeBaseId)
        source_ids = @($script:dataSourceIds)
        agent_enabled = $true
        agent_id = $AgentId
        channel = 'web'
        disable_title = $true
    }
    $response = Invoke-AcceptanceHttp 'POST' "/agent-chat/$([Uri]::EscapeDataString($AgentSessionId))" $request 'text/event-stream'
    if ([int]$response.StatusCode -lt 200 -or [int]$response.StatusCode -ge 300) {
        Stop-Acceptance 'agent_ui_http_failure' "The UI agent endpoint returned HTTP $([int]$response.StatusCode); response content was suppressed."
    }
    $eventTypes = [System.Collections.Generic.List[string]]::new()
    $sawComplete = $false
    foreach ($line in ($response.Content -split "`r?`n")) {
        if (-not $line.StartsWith('data: ')) { continue }
        try { $event = $line.Substring(6) | ConvertFrom-Json -Depth 20 -NoEnumerate } catch {
            Stop-Acceptance 'agent_ui_invalid_event' 'The UI agent endpoint returned an invalid JSON event; event content was suppressed.'
        }
        if ($sawComplete) { Stop-Acceptance 'agent_ui_event_after_complete' 'The UI agent stream contained events after its terminal success event.' }
        $kindValue = Get-Field $event 'response_type'
        if ($kindValue -isnot [string]) {
            Stop-Acceptance 'agent_ui_event_invalid' 'The UI agent stream omitted its typed response event; event content was suppressed.'
        }
        $kind = [string]$kindValue
        Assert-AgentResponseType $kind
        $eventTypeValue = Get-Field $event 'type'
        $hasEventType = $false
        if ($event -is [System.Collections.IDictionary]) {
            $hasEventType = $event.Contains('type')
        } elseif ($null -ne $event) {
            $hasEventType = $null -ne $event.PSObject.Properties['type']
        }
        if ($hasEventType) {
            if ($eventTypeValue -isnot [string]) { Stop-Acceptance 'agent_ui_event_invalid' 'The UI agent stream contained an invalid event type; event content was suppressed.' }
            Assert-AgentResponseType ([string]$eventTypeValue)
            if ([string]$eventTypeValue -cne $kind) { Stop-Acceptance 'agent_ui_event_invalid' 'The UI agent stream contained inconsistent event types; event content was suppressed.' }
        }
        if ($kind -eq 'error') {
            Stop-Acceptance 'agent_ui_stream_error' 'The UI agent stream reported an error; event content was suppressed.'
        }
        $eventTypes.Add($kind)
        if ($kind -eq 'complete') {
            $doneValue = Get-Field $event 'done'
            if ($doneValue -isnot [bool] -or $doneValue -ne $true) { Stop-Acceptance 'agent_ui_incomplete_stream' 'The UI agent stream omitted the terminal completion marker.' }
            $sawComplete = $true
        }
    }
    if ($eventTypes.Count -eq 0) { Stop-Acceptance 'agent_ui_empty_stream' 'The UI agent endpoint completed without a verifiable response event.' }
    if (-not $sawComplete) { Stop-Acceptance 'agent_ui_incomplete_stream' 'The UI agent stream ended without a terminal completion event.' }
    $script:report.agent_ui_smoke.status = 'completed'
    $script:report.agent_ui_smoke.event_count = $eventTypes.Count
    $script:report.agent_ui_smoke.event_types = @($eventTypes.ToArray() | Select-Object -Unique)
}

try {
    if ($PSVersionTable.PSVersion.Major -lt 7) { Stop-Acceptance 'powershell_version_unsupported' 'PowerShell 7 or later is required.' }
    if ($ConfiguredInputTokenLimit -gt $ProviderDocumentedHardLimit) { Stop-Acceptance 'model_limit_invalid' 'Configured model input tokens exceed the provider-documented hard limit.' }
    foreach ($value in @($ModelIdentifier, $ProviderLimitReference, $HardwareDescription)) {
        if ([string]::IsNullOrWhiteSpace($value) -or $value -match '[\r\n]') { Stop-Acceptance 'model_profile_invalid' 'Model, provider-limit reference, and hardware fields must be non-empty single-line values.' }
    }
    if ($ModelIdentifier.Length -gt 200 -or $ProviderLimitReference.Length -gt 512 -or $HardwareDescription.Length -gt 512) {
        Stop-Acceptance 'model_profile_invalid' 'Model and hardware report fields exceed their bounded lengths.'
    }
    if ($ProviderLimitReference -match '(?i)(access.?token|api.?key|password|secret)=') {
        Stop-Acceptance 'provider_reference_invalid' 'Provider-limit reference must not contain credential-like query material.'
    }
    $providerReferenceUri = $null
    if ([Uri]::TryCreate($ProviderLimitReference, [UriKind]::Absolute, [ref]$providerReferenceUri) -and
        ($providerReferenceUri.UserInfo -or $providerReferenceUri.Query)) {
        Stop-Acceptance 'provider_reference_invalid' 'Provider-limit URL must not contain credentials or query material.'
    }
    $script:report.model.provider_limit_reference = $ProviderLimitReference
    $token = [Environment]::GetEnvironmentVariable('WEKNORA_ACCESS_TOKEN', 'Process')
    if ([string]::IsNullOrWhiteSpace($token)) { Stop-Acceptance 'access_token_missing' 'Set WEKNORA_ACCESS_TOKEN in the current process environment; the token is never read from a file or command argument.' }
    $parsedBase = $null
    if (-not [Uri]::TryCreate($BaseUrl, [UriKind]::Absolute, [ref]$parsedBase) -or
        $parsedBase.Scheme -notin @('https', 'http') -or $parsedBase.UserInfo -or $parsedBase.Query -or $parsedBase.Fragment -or
        ($parsedBase.Scheme -eq 'http' -and $parsedBase.Host -notin @('localhost', '127.0.0.1', '::1'))) {
        Stop-Acceptance 'base_url_invalid' 'BaseUrl must be an HTTPS origin without credentials/query/fragment (HTTP is allowed only for loopback fake-server tests).'
    }
    if ($parsedBase.AbsolutePath.Trim('/') -and $parsedBase.AbsolutePath.Trim('/') -ne 'api/v1') {
        Stop-Acceptance 'base_url_invalid' 'BaseUrl must be an origin, optionally ending in /api/v1.'
    }
    $root = $parsedBase.GetLeftPart([UriPartial]::Authority)
    $script:baseUri = [Uri]::new($root.TrimEnd('/') + '/api/v1/')
    $script:accessToken = $token
    $token = $null
    $clientHandler = [System.Net.Http.HttpClientHandler]::new()
    $clientHandler.AllowAutoRedirect = $false
    $clientHandler.UseCookies = $false
    $script:httpClient = [System.Net.Http.HttpClient]::new($clientHandler)
    $script:httpClient.Timeout = [System.Threading.Timeout]::InfiniteTimeSpan
    $script:runStopwatch = [System.Diagnostics.Stopwatch]::StartNew()

    if (-not (Test-Path -LiteralPath $QuestionsFile -PathType Leaf)) { Stop-Acceptance 'question_file_missing' 'The question manifest file does not exist.' }
    if ((Get-Item -LiteralPath $QuestionsFile).Length -gt 4194304) { Stop-Acceptance 'question_file_too_large' 'Question manifest exceeds the 4 MiB limit.' }
    $manifest = Get-Content -LiteralPath $QuestionsFile -Raw | ConvertFrom-Json -Depth 40 -NoEnumerate
    if ((Get-Field $manifest 'schema_version') -ne 1) { Stop-Acceptance 'question_schema_invalid' 'Question manifest schema_version must be 1.' }
    $manifestBytes = [System.IO.File]::ReadAllBytes([System.IO.Path]::GetFullPath($QuestionsFile))
    if ($manifestBytes.Length -gt 4194304) { Stop-Acceptance 'question_file_too_large' 'Question manifest exceeds the 4 MiB limit.' }
    $manifestHash = [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($manifestBytes)).ToLowerInvariant()
    $manifestStatus = [string](Get-Field $manifest 'status')
    $script:report.question_manifest = [ordered]@{ sha256 = $manifestHash; status = if ($manifestStatus) { $manifestStatus } else { 'unapproved' }; approval_verified = $false }
    $allowedManifestStatus = $manifestStatus -in @('human_confirmed', 'approved_for_acceptance')
    if (-not $allowedManifestStatus -and $ApprovalMarkerFile) {
        if (-not (Test-Path -LiteralPath $ApprovalMarkerFile -PathType Leaf) -or (Get-Item -LiteralPath $ApprovalMarkerFile).Length -gt 65536) { Stop-Acceptance 'approval_marker_missing' 'The explicit approval marker file is missing or exceeds its 64 KiB limit.' }
        $marker = Get-Content -LiteralPath $ApprovalMarkerFile -Raw | ConvertFrom-Json -Depth 20 -NoEnumerate
        if ((Get-Field $marker 'schema_version') -ne 1 -or (Get-Field $marker 'status') -ne 'approved' -or
            (Get-Field $marker 'question_manifest_sha256') -ne $manifestHash -or
            [string]::IsNullOrWhiteSpace([string](Get-Field $marker 'approved_by')) -or
            [string]::IsNullOrWhiteSpace([string](Get-Field $marker 'approved_at_utc'))) {
            Stop-Acceptance 'approval_marker_invalid' 'Approval marker must bind this exact question-manifest SHA-256 and name its human approver and timestamp.'
        }
        $allowedManifestStatus = $true
        $script:report.question_manifest.approval_verified = $true
    }
    if (-not $allowedManifestStatus -and -not $AllowDraftQuestions) {
        Stop-Acceptance 'questions_not_approved' 'The question manifest is still a draft; supply its human approval marker before acceptance scoring.'
    }
    if ($Publish -and -not $allowedManifestStatus) { Stop-Acceptance 'questions_not_approved' 'Publishing cannot be requested for an unapproved question manifest.' }
    $script:report.question_manifest.scoring_allowed = $allowedManifestStatus

    $script:repositoryMappings = @{}
    if ($RepositorySourceMap) {
        if (-not (Test-Path -LiteralPath $RepositorySourceMap -PathType Leaf)) { Stop-Acceptance 'source_mapping_missing' 'The repository-to-source mapping file does not exist.' }
        $mappingManifest = Get-Content -LiteralPath $RepositorySourceMap -Raw | ConvertFrom-Json -Depth 30 -NoEnumerate
        if ((Get-Field $mappingManifest 'schema_version') -ne 1 -or (Get-Field $mappingManifest 'status') -ne 'approved' -or
            (Get-Field $mappingManifest 'knowledge_base_id') -ne $KnowledgeBaseId) {
            Stop-Acceptance 'source_mapping_invalid' 'Repository source mapping must be schema v1, explicitly approved, and scoped to the selected knowledge base.'
        }
        $mappingEntries = @((Get-Field $mappingManifest 'repositories') | Where-Object { $null -ne $_ })
        if ($mappingEntries.Count -eq 0) {
            $mappingObject = Get-Field $mappingManifest 'mappings'
            if ($mappingObject -is [System.Collections.IDictionary]) {
                foreach ($repositoryKey in $mappingObject.Keys) {
                    $entry = $mappingObject[$repositoryKey]
                    $mappingEntries += [pscustomobject]@{ repository_id = [string]$repositoryKey; source_id = Get-Field $entry 'source_id'; snapshot_id = Get-Field $entry 'snapshot_id'; commit_sha = Get-Field $entry 'commit_sha'; hash_mode = Get-Field $entry 'hash_mode'; knowledge_base_id = Get-Field $entry 'knowledge_base_id' }
                }
            } elseif ($null -ne $mappingObject) {
                foreach ($property in $mappingObject.PSObject.Properties) {
                    $entry = $property.Value
                    $mappingEntries += [pscustomobject]@{ repository_id = $property.Name; source_id = Get-Field $entry 'source_id'; snapshot_id = Get-Field $entry 'snapshot_id'; commit_sha = Get-Field $entry 'commit_sha'; hash_mode = Get-Field $entry 'hash_mode'; knowledge_base_id = Get-Field $entry 'knowledge_base_id' }
                }
            }
        }
        foreach ($mapping in $mappingEntries) {
            $repositoryId = [string](Get-Field $mapping 'repository_id')
            $sourceId = [string](Get-Field $mapping 'source_id')
            $snapshotId = [string](Get-Field $mapping 'snapshot_id')
            $commit = [string](Get-Field $mapping 'commit_sha')
            $mappingHashMode = [string](Get-Field $mapping 'hash_mode')
            if (-not $repositoryId -or $script:repositoryMappings.ContainsKey($repositoryId) -or -not $sourceId -or -not $snapshotId -or -not $commit -or ($mappingHashMode -and $mappingHashMode -notin @('sha256_utf8_lf', 'sha256_raw_utf8'))) {
                Stop-Acceptance 'source_mapping_invalid' 'Each mapped repository must have a unique ID, source, target snapshot, commit, and explicit supported hash mode.'
            }
            $script:repositoryMappings[$repositoryId] = $mapping
        }
        if ($script:repositoryMappings.Count -eq 0) { Stop-Acceptance 'source_mapping_empty' 'The approved repository source map contains no repository mappings.' }
    }

    $repositoryCommits = @{}
    foreach ($repository in @(Get-Field $manifest 'repositories')) {
        $repositoryId = [string](Get-Field $repository 'id')
        $repositoryCommit = [string](Get-Field $repository 'commit')
        if ($repositoryId -and $repositoryCommit) { $repositoryCommits[$repositoryId] = $repositoryCommit }
    }
    $rawQuestions = @(Get-Field $manifest 'questions')
    if ($rawQuestions.Count -ne 30) { Stop-Acceptance 'question_count_invalid' 'Question manifest must contain exactly 30 manually verified questions.' }
    $questions = [System.Collections.Generic.List[object]]::new()
    $categoryCounts = @{ symbol_path = 0; business_chain = 0; frontend_sql = 0 }
    $questionIds = @{}
    foreach ($question in $rawQuestions) {
        $id = [string](Get-Field $question 'question_id')
        if (-not $id) { $id = [string](Get-Field $question 'id') }
        $category = [string](Get-Field $question 'category')
        $query = [string](Get-Field $question 'query')
        if (-not $query) { $query = [string](Get-Field $question 'question') }
        if ($category -eq 'business_flow') { $category = 'business_chain' }
        $evidenceItems = @((Get-Field $question 'evidence') | Where-Object { $null -ne $_ })
        if ($evidenceItems.Count -eq 0) {
            $expected = Get-Field $question 'expected'
            if ($null -ne $expected) { $evidenceItems = @($expected) }
        }
        if (-not $id -or $id.Length -gt 128 -or $questionIds.ContainsKey($id)) { Stop-Acceptance 'question_id_invalid' 'Question IDs must be unique, non-empty, and at most 128 characters.' }
        if (-not $categoryCounts.ContainsKey($category)) { Stop-Acceptance 'question_category_invalid' 'Question categories must be symbol_path, business_chain, or frontend_sql.' }
        if (-not $query -or $query.Length -gt 4000) { Stop-Acceptance 'question_query_invalid' "Question '$id' must contain a bounded non-empty query." }
        if ($evidenceItems.Count -eq 0 -or $evidenceItems.Count -gt 20) { Stop-Acceptance 'question_evidence_invalid' "Question '$id' must name 1 to 20 expected evidence spans." }
        $normalizedEvidence = [System.Collections.Generic.List[object]]::new()
        $evidenceOrdinal = 0
        foreach ($evidence in $evidenceItems) {
            $evidenceOrdinal++
            $pathValue = Get-Field $evidence 'path'
            $path = if ($pathValue -is [string]) { $pathValue } else { $null }
            $sha = [string](Get-Field $evidence 'sha256')
            $start = Get-Field $evidence 'start_line'
            $end = Get-Field $evidence 'end_line'
            $repositoryId = [string](Get-Field $evidence 'repository')
            if (-not $repositoryId) { $repositoryId = [string](Get-Field $question 'repository') }
            if (-not (Test-CanonicalGitRelativePath $path) -or $sha -notmatch '^[0-9a-fA-F]{64}$' -or [int]$start -lt 1 -or [int]$end -lt [int]$start) {
                Stop-Acceptance 'question_evidence_invalid' "Question '$id' evidence must identify a relative path, valid one-based line range, and SHA-256."
            }
            $mapping = if ($repositoryId) { Get-RepositoryMapping $repositoryId } else { $null }
            if ($repositoryId -and -not $mapping) { Stop-Acceptance 'source_mapping_missing' "Question '$id' references a repository without an approved source mapping." }
            $sourceId = [string](Get-Field $evidence 'source_id')
            if (-not $sourceId -and $mapping) { $sourceId = [string](Get-Field $mapping 'source_id') }
            if (-not $sourceId) { $sourceId = $DataSourceId }
            if (-not $sourceId) { Stop-Acceptance 'source_mapping_missing' "Question '$id' evidence repository has no explicit source mapping." }
            if ($mapping -and (Get-Field $mapping 'knowledge_base_id') -and (Get-Field $mapping 'knowledge_base_id') -ne $KnowledgeBaseId) { Stop-Acceptance 'source_mapping_scope_mismatch' "Question '$id' repository mapping is outside the selected knowledge base." }
            if ($mapping -and (Get-Field $mapping 'source_id') -ne $sourceId) { Stop-Acceptance 'source_mapping_scope_mismatch' "Question '$id' evidence source differs from its approved repository mapping." }
            $snapshotId = [string](Get-Field $evidence 'snapshot_id')
            $snapshotIdExplicit = -not [string]::IsNullOrWhiteSpace($snapshotId)
            if (-not $snapshotId -and $mapping) {
                $snapshotId = [string](Get-Field $mapping 'snapshot_id')
                $snapshotIdExplicit = -not [string]::IsNullOrWhiteSpace($snapshotId)
            }
            $commit = [string](Get-Field $evidence 'commit_sha')
            if (-not $commit -and $repositoryId -and $repositoryCommits.ContainsKey($repositoryId)) { $commit = [string]$repositoryCommits[$repositoryId] }
            if (-not $commit -and $mapping) { $commit = [string](Get-Field $mapping 'commit_sha') }
            if ($Publish -and -not $RepositorySourceMap -and -not $commit) {
                Stop-Acceptance 'publish_commit_required' "Question '$id' evidence must pin a commit before the legacy source is published."
            }
            $mappingMode = [string](Get-Field $mapping 'hash_mode')
            $evidenceHashMode = [string](Get-Field $evidence 'hash_mode')
            $datasetHashMode = [string](Get-Field $manifest 'hash_mode')
            if (-not $evidenceHashMode) { $evidenceHashMode = if ($datasetHashMode) { $datasetHashMode } else { $HashMode } }
            if ($evidenceHashMode -notin @('sha256_utf8_lf', 'sha256_raw_utf8')) { Stop-Acceptance 'question_hash_mode_invalid' "Question '$id' has no explicit supported evidence hash mode." }
            if ($datasetHashMode -and $evidenceHashMode -cne $datasetHashMode) { Stop-Acceptance 'question_hash_mode_mismatch' "Question '$id' evidence hash mode differs from its question-bank contract." }
            if ($mappingMode -and $evidenceHashMode -cne $mappingMode) { Stop-Acceptance 'question_hash_mode_mismatch' "Question '$id' evidence hash mode differs from its approved repository mapping." }
            if ($HashMode -and $HashMode -cne $evidenceHashMode) { Stop-Acceptance 'question_hash_mode_mismatch' "Question '$id' evidence hash mode differs from the command-line hash-mode assertion." }
            if ($mapping -and $commit -cne [string](Get-Field $mapping 'commit_sha')) { Stop-Acceptance 'source_mapping_commit_mismatch' "Question '$id' evidence commit differs from its approved repository mapping." }
            if ($repositoryId -and $repositoryCommits.ContainsKey($repositoryId) -and $commit -cne [string]$repositoryCommits[$repositoryId]) { Stop-Acceptance 'question_commit_mismatch' "Question '$id' evidence commit differs from the question bank's pinned repository commit." }
            $evidenceId = '{0}-{1:D2}' -f $id, $evidenceOrdinal
            $normalizedEvidence.Add([ordered]@{ evidence_id = $evidenceId; repository = $repositoryId; source_id = $sourceId; snapshot_id = $snapshotId; snapshot_id_explicit = $snapshotIdExplicit; commit_sha = $commit; path = $path; start_line = [int]$start; end_line = [int]$end; sha256 = $sha.ToLowerInvariant(); hash_mode = $evidenceHashMode; symbol = Get-Field $evidence 'symbol' })
        }
        $questionRepositories = @($normalizedEvidence | ForEach-Object { [string](Get-Field $_ 'repository') } | Select-Object -Unique)
        $questionSources = @($normalizedEvidence | ForEach-Object { [string](Get-Field $_ 'source_id') } | Select-Object -Unique)
        if ($questionRepositories.Count -gt 1 -or $questionSources.Count -gt 1) {
            if ($questionRepositories -contains '' -or $questionSources.Count -ne $questionRepositories.Count) {
                Stop-Acceptance 'question_cross_repository_mapping_invalid' 'Cross-repository evidence must resolve through explicit mappings to distinct source identities.'
            }
        }
        $questionIds[$id] = $true
        $categoryCounts[$category]++
        $questions.Add([ordered]@{ question_id = $id; category = $category; query = $query; expected_evidence = @($normalizedEvidence.ToArray()) })
    }
    $datasetHashMode = [string](Get-Field $manifest 'hash_mode')
    if ((@(Get-Field $manifest 'repositories')).Count -gt 0 -and $datasetHashMode -notin @('sha256_utf8_lf', 'sha256_raw_utf8')) {
        Stop-Acceptance 'question_hash_mode_invalid' 'A repository-backed question bank must explicitly declare its dataset-level hash_mode.'
    }
    if ($datasetHashMode -and $HashMode -and $datasetHashMode -cne $HashMode) { Stop-Acceptance 'question_hash_mode_mismatch' 'The command-line hash mode differs from the gold question bank contract.' }
    $script:report.model.evidence_hash_mode = if ($datasetHashMode) { $datasetHashMode } else { $HashMode }
    if ($categoryCounts.symbol_path -ne 10 -or $categoryCounts.business_chain -ne 10 -or $categoryCounts.frontend_sql -ne 10) {
        Stop-Acceptance 'question_category_counts_invalid' 'The 30 questions must be split into exactly 10 symbol/path, 10 business-flow, and 10 frontend/SQL cases.'
    }

    if ($MeasurementsFile) {
        if (-not (Test-Path -LiteralPath $MeasurementsFile -PathType Leaf) -or (Get-Item -LiteralPath $MeasurementsFile).Length -gt 2097152) { Stop-Acceptance 'measurements_file_missing' 'The optional measurement input file is missing or exceeds its 2 MiB limit.' }
        $measurements = Get-Content -LiteralPath $MeasurementsFile -Raw | ConvertFrom-Json -Depth 40 -NoEnumerate
        if ((Get-Field $measurements 'schema_version') -ne 1) { Stop-Acceptance 'measurements_schema_invalid' 'Measurement input schema_version must be 1.' }
        $script:fullRunMeasurement = Get-Field $measurements 'full_run'
        $runs = @(Get-Field $measurements 'incremental_runs')
        foreach ($expectedCount in @(1, 10, 100)) {
            $matches = @($runs | Where-Object { (Get-Field $_ 'changed_file_count') -eq $expectedCount })
            if ($matches.Count -gt 1) { Stop-Acceptance 'incremental_metrics_invalid' "Measurement input repeats the $expectedCount-file scenario." }
            if ($matches.Count -eq 1) {
                $run = $matches[0]
                if ((Get-Field $run 'model_identifier') -ne $ModelIdentifier -or (Get-Field $run 'tokenizer') -ne $Tokenizer -or (Get-Field $run 'context_limit_tokens') -ne $ConfiguredInputTokenLimit) {
                    Stop-Acceptance 'incremental_budget_mismatch' "The $expectedCount-file measurement does not use the declared model/tokenizer/input budget."
                }
                $runStatus = [string](Get-Field $run 'status')
                if ($runStatus -notin @('measured', 'unknown')) { Stop-Acceptance 'incremental_metrics_invalid' "The $expectedCount-file scenario status must be measured or unknown." }
                $metrics = Get-ValidatedMeasurementMetrics $run "incremental.$expectedCount" 'incremental_metrics_invalid'
                $measurementSummary = Get-MeasurementSummary $run
                $incrementalInput = [ordered]@{ model_identifier = $ModelIdentifier; tokenizer = $Tokenizer; context_limit_tokens = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }
                $incrementalOutput = [ordered]@{ source_id = Get-Field $run 'source_id'; snapshot_id = Get-Field $run 'snapshot_id'; commit_sha = Get-Field $run 'commit_sha'; selected_files = $metrics.scalar.selected_files; selected_bytes = $metrics.scalar.selected_bytes; chunk_count = $metrics.scalar.chunk_count; phase_duration_ms = $metrics.phase_duration_ms; elapsed_ms = $metrics.scalar.elapsed_ms; peak_memory_bytes = $metrics.scalar.peak_memory_bytes; estimated_input_tokens = $metrics.scalar.estimated_input_tokens; actual_input_tokens = $metrics.scalar.actual_input_tokens; embedding_calls = $metrics.scalar.embedding_calls; generation_calls = $metrics.scalar.generation_calls }
                $script:report.incremental_runs = @($script:report.incremental_runs | Where-Object { $_.changed_file_count -ne $expectedCount }) + @([ordered]@{ changed_file_count = $expectedCount; status = $measurementSummary.status; completeness = $measurementSummary.completeness; provided_status = $runStatus; metric_status = $measurementSummary.metric_status; input = $incrementalInput; output = $incrementalOutput })
            }
        }
        $baseline = Get-Field $measurements 'text_baseline'
        if ($null -ne $baseline) {
            if ((Get-Field $baseline 'model_identifier') -ne $ModelIdentifier -or (Get-Field $baseline 'tokenizer') -ne $Tokenizer -or (Get-Field $baseline 'budget_tokens') -ne $ConfiguredInputTokenLimit) {
                Stop-Acceptance 'baseline_budget_mismatch' 'Text baseline must use the exact declared model, tokenizer, and configured input-token budget.'
            }
            $baselineStatus = [string](Get-Field $baseline 'status')
            if ($baselineStatus -notin @('measured', 'unknown')) { Stop-Acceptance 'baseline_metrics_invalid' 'Text baseline status must be measured or unknown.' }
            $baselineMetrics = Get-ValidatedMeasurementMetrics $baseline 'text_baseline' 'baseline_metrics_invalid'
            $baselineSummary = Get-MeasurementSummary $baseline
            $script:report.text_baseline = [ordered]@{
                status = $baselineSummary.status
                completeness = $baselineSummary.completeness
                provided_status = $baselineStatus
                metric_status = $baselineSummary.metric_status
                inputs = [ordered]@{ tokenizer = $Tokenizer; model_identifier = $ModelIdentifier; same_budget_input_token_limit = $ConfiguredInputTokenLimit; hardware = $HardwareDescription }
                outputs = [ordered]@{ selected_files = $baselineMetrics.scalar.selected_files; selected_bytes = $baselineMetrics.scalar.selected_bytes; chunk_count = $baselineMetrics.scalar.chunk_count; phase_duration_ms = $baselineMetrics.phase_duration_ms; elapsed_ms = $baselineMetrics.scalar.elapsed_ms; peak_memory_bytes = $baselineMetrics.scalar.peak_memory_bytes; estimated_input_tokens = $baselineMetrics.scalar.estimated_input_tokens; actual_input_tokens = $baselineMetrics.scalar.actual_input_tokens; embedding_calls = $baselineMetrics.scalar.embedding_calls; generation_calls = $baselineMetrics.scalar.generation_calls }
            }
        }
    }

    $script:dataSourceIds = @($questions | ForEach-Object { $_.expected_evidence } | ForEach-Object { $_.source_id } | Where-Object { $_ } | Select-Object -Unique)
    if ($script:dataSourceIds.Count -eq 0 -or $script:dataSourceIds.Count -gt 20) { Stop-Acceptance 'source_scope_invalid' 'The 30 questions must map to 1-20 explicit sources in one knowledge base.' }
    if ($DataSourceId -and $DataSourceId -notin $script:dataSourceIds) { Stop-Acceptance 'source_scope_mismatch' 'The optional single-source selector is not used by any expected evidence item.' }
    $script:report.scope.data_source_ids = @($script:dataSourceIds)
    $script:report.scope.data_source_id = if ($script:dataSourceIds.Count -eq 1) { $script:dataSourceIds[0] } else { $null }

    $script:previewBySource = @{}
    $previewReports = [System.Collections.Generic.List[object]]::new()
    $allParserReady = $true
    foreach ($sourceId in $script:dataSourceIds) {
        $source = Invoke-AcceptanceJson 'GET' "/datasource/$(Get-PathSegment $sourceId)"
        if ((Get-Field $source 'id') -ne $sourceId -or (Get-Field $source 'knowledge_base_id') -ne $KnowledgeBaseId -or (Get-Field $source 'type') -ne 'gitlab') {
            Stop-Acceptance 'source_scope_mismatch' 'A mapped datasource is not a GitLab source in the explicitly selected knowledge base.'
        }
        $lifecycle = Get-Field $source 'source_lifecycle'
        if ((Get-Field $lifecycle 'binding_state') -and (Get-Field $lifecycle 'binding_state') -ne 'bound') { Stop-Acceptance 'source_not_bound' 'A mapped source is not bound for reads.' }
        if ((Get-Field $lifecycle 'query_enabled') -eq $false) { Stop-Acceptance 'source_reads_disabled' 'A mapped source has reads disabled.' }
        $config = Get-Field $source 'config'
        $settings = Get-Field $config 'settings'
        if ((Get-Field $settings 'content_mode') -ne 'source' -or $null -eq (Get-Field $settings 'projects')) { Stop-Acceptance 'source_mode_not_configured' 'A mapped source is not configured with explicit GitLab source-mode project paths.' }
        Assert-NoSensitiveSettings $settings
        $previewEnvelope = Invoke-AcceptanceJson 'POST' "/datasource/$(Get-PathSegment $sourceId)/source-preview" ([ordered]@{ settings = $settings })
        $preview = Get-Field $previewEnvelope 'data'
        $files = @(Get-Field $preview 'files')
        if ($files.Count -gt 10000) { Stop-Acceptance 'preview_inventory_too_large' 'Source preview exceeded the configured 10,000-file report limit.' }
        $checks = @(Get-Field $preview 'checks')
        $parserCheck = @($checks | Where-Object { (Get-Field $_ 'name') -eq 'parser' }) | Select-Object -First 1
        $parserReady = $null -ne $parserCheck -and (Get-Field $parserCheck 'ready') -eq $true
        $allParserReady = $allParserReady -and $parserReady
        $selectedFiles = @($files | Where-Object { (Get-Field $_ 'status') -eq 'included' })
        $selectedBytes = [int64]0
        foreach ($file in $selectedFiles) {
            $size = Get-Field $file 'size'
            if ($null -eq $size -or [int64]$size -lt 0) { Stop-Acceptance 'preview_size_missing' 'A selected preview file has no valid measured size.' }
            $selectedBytes += [int64]$size
        }
        $safeFiles = @($files | ForEach-Object { [ordered]@{ path = Get-Field $_ 'path'; blob_sha = Get-Field $_ 'blob_sha'; size = Get-Field $_ 'size'; status = Get-Field $_ 'status' } })
        if (-not $parserReady) { Stop-Acceptance 'parser_not_ready' 'A source preview did not report a ready parser for the selected source.' }
        if ((Get-Field $preview 'can_sync') -ne $true) { Stop-Acceptance 'preview_not_ready' 'A mapped source preview is not publishable; no sync was started.' }
        foreach ($mapping in $script:repositoryMappings.Values) {
            if ((Get-Field $mapping 'source_id') -eq $sourceId -and (Get-Field $mapping 'commit_sha') -cne (Get-Field $preview 'commit_sha')) {
                Stop-Acceptance 'preview_commit_mismatch' 'A source preview differs from the commit approved in its repository mapping.'
            }
        }
        $script:previewBySource[$sourceId] = [pscustomobject]@{ preview = $preview; settings = $settings }
        $previewReports.Add([ordered]@{
            source_id = $sourceId; project_id = Get-Field $preview 'project_id'; branch = Get-Field $preview 'branch'; commit_sha = Get-Field $preview 'commit_sha'; rules_version = Get-Field $preview 'rules_version'; can_sync = Get-Field $preview 'can_sync'; parser_ready = $parserReady; checks = @($checks | ForEach-Object { [ordered]@{ name = Get-Field $_ 'name'; ready = Get-Field $_ 'ready' } }); selected_files = $selectedFiles.Count; selected_bytes = $selectedBytes; inventory = $safeFiles
        })
    }

    $kbEnvelope = Invoke-AcceptanceJson 'GET' "/knowledge-bases/$(Get-PathSegment $KnowledgeBaseId)"
    $kb = Get-Field $kbEnvelope 'data'
    if ((Get-Field $kb 'id') -ne $KnowledgeBaseId -or (Get-Field $kb 'embedding_model_id') -ne $ModelIdentifier) {
        Stop-Acceptance 'model_scope_mismatch' 'The explicitly named model does not match the selected knowledge base embedding model.'
    }
    $modelEnvelope = Invoke-AcceptanceJson 'GET' "/models/$(Get-PathSegment $ModelIdentifier)"
    $model = Get-Field $modelEnvelope 'data'
    $modelParameters = Get-Field $model 'parameters'
    $embeddingParameters = Get-Field $modelParameters 'embedding_parameters'
    if ((Get-Field $model 'id') -ne $ModelIdentifier -or
        (Get-Field $model 'type') -ne 'Embedding' -or
        (Get-Field $embeddingParameters 'tokenizer') -ne $Tokenizer -or
        [int](Get-Field $embeddingParameters 'max_input_tokens') -ne $ConfiguredInputTokenLimit) {
        Stop-Acceptance 'model_profile_mismatch' 'The model API tokenizer/type/input limit differs from the explicit acceptance profile.'
    }

    $script:report.parser.ready = $allParserReady
    $script:report.preview = @($previewReports.ToArray())

    if ($Publish) {
        if ($RepositorySourceMap -or $script:dataSourceIds.Count -ne 1) { Stop-Acceptance 'publish_scope_requires_review' 'Publish mode supports only one explicitly selected legacy source; mapped representative runs are read-only against their approved target snapshots.' }
        $publishSourceId = [string]$script:dataSourceIds[0]
        $publishPreviewCommit = [string](Get-Field $script:previewBySource[$publishSourceId].preview 'commit_sha')
        foreach ($question in $questions) {
            foreach ($expectedItem in $question.expected_evidence) {
                if ((Get-Field $expectedItem 'source_id') -cne $publishSourceId -or
                    (Get-Field $expectedItem 'commit_sha') -cne $publishPreviewCommit) {
                    Stop-Acceptance 'publish_input_scope_mismatch' 'Legacy publish requires every evidence source and pinned commit to match the selected source preview.'
                }
            }
        }
        $script:report.publish.status = 'starting'
        $initialLog = Invoke-AcceptanceJson 'POST' "/datasource/$(Get-PathSegment $publishSourceId)/sync" ([ordered]@{})
        $published = Wait-ForPublishedRun $initialLog $publishSourceId
        $publishedRuns = @($published)
    } else {
        $script:report.publish.status = 'not_requested'
        $publishedRuns = @($script:dataSourceIds | ForEach-Object { Get-PublishedRunFromLogs $_ })
    }
    $script:publishedBySource = @{}
    $snapshotReports = [System.Collections.Generic.List[object]]::new()
    $publishTelemetry = [System.Collections.Generic.List[object]]::new()
    foreach ($published in $publishedRuns) {
        $snapshot = $published.snapshot
        $sourceId = [string](Get-Field $snapshot 'data_source_id')
        $snapshotId = [string](Get-Field $snapshot 'id')
        $commitSha = [string](Get-Field $snapshot 'commit_sha')
        if (-not $snapshotId -or -not $commitSha -or $sourceId -notin $script:dataSourceIds) { Stop-Acceptance 'published_snapshot_incomplete' 'A selected source publication is missing its stable source, snapshot ID, or commit SHA.' }
        if ($commitSha -cne [string](Get-Field $script:previewBySource[$sourceId].preview 'commit_sha')) {
            $failureCode = if ($Publish) { 'publish_commit_mismatch' } else { 'published_snapshot_stale' }
            Stop-Acceptance $failureCode 'A published commit differs from the fixed commit returned by its source preview.'
        }
        foreach ($mapping in $script:repositoryMappings.Values) {
            if ((Get-Field $mapping 'source_id') -eq $sourceId -and
                ((Get-Field $mapping 'snapshot_id') -cne $snapshotId -or (Get-Field $mapping 'commit_sha') -cne $commitSha)) {
                Stop-Acceptance 'published_snapshot_mapping_mismatch' 'A published snapshot does not match its approved repository-to-source target.'
            }
        }
        $script:publishedBySource[$sourceId] = $published
        $snapshotReports.Add([ordered]@{ source_id = $sourceId; knowledge_base_id = $KnowledgeBaseId; snapshot_id = $snapshotId; commit_sha = $commitSha; state = Get-Field $snapshot 'state'; previous_snapshot_id = Get-Field $snapshot 'previous_snapshot_id'; file_count = Get-Field $snapshot 'file_count'; telemetry = $published.telemetry })
        if ($published.telemetry) { $publishTelemetry.Add([ordered]@{ source_id = $sourceId; snapshot_id = $snapshotId; telemetry = $published.telemetry }) }
        if (-not $script:report.parser.processing_version) { $script:report.parser.processing_version = Get-Field $snapshot 'processing_version' }
    }
    if ($Publish) {
        $publishSourceId = [string]$script:dataSourceIds[0]
        $publishedSnapshot = $script:publishedBySource[$publishSourceId].snapshot
        $actualSnapshotId = [string](Get-Field $publishedSnapshot 'id')
        $actualCommit = [string](Get-Field $publishedSnapshot 'commit_sha')
        foreach ($question in $questions) {
            foreach ($expectedItem in $question.expected_evidence) {
                if ((Get-Field $expectedItem 'source_id') -cne $publishSourceId -or
                    (Get-Field $expectedItem 'commit_sha') -cne $actualCommit) {
                    Stop-Acceptance 'publish_input_scope_mismatch' 'The successful publication does not match the source and commit pinned by legacy evidence.'
                }
                if ((Get-Field $expectedItem 'snapshot_id_explicit') -eq $true) {
                    if ((Get-Field $expectedItem 'snapshot_id') -cne $actualSnapshotId) {
                        Stop-Acceptance 'publish_snapshot_explicit_mismatch' 'The newly published snapshot differs from an explicitly pinned legacy evidence snapshot; the pin was not rewritten.'
                    }
                } else {
                    $expectedItem.snapshot_id = $actualSnapshotId
                }
            }
        }
    }
    Set-FullRunMeasurement $script:fullRunMeasurement
    $script:report.scope.snapshots = @($snapshotReports.ToArray())
    if ($snapshotReports.Count -eq 1) {
        $script:report.scope.snapshot_id = $snapshotReports[0].snapshot_id
        $script:report.scope.commit_sha = $snapshotReports[0].commit_sha
        $script:report.publish.snapshot_id = $snapshotReports[0].snapshot_id
        $script:report.publish.commit_sha = $snapshotReports[0].commit_sha
    }
    $script:report.publish.telemetry = @($publishTelemetry.ToArray())
    if ($Publish) { $script:report.publish.sync_log_id = Get-Field $publishedRuns[0].log 'id'; $script:report.publish.status = 'success' }

    foreach ($question in $questions) {
        $result = Invoke-QuestionSearch $question
        $script:report.question_results += $result
    }
    $matchedCount = @($script:report.question_results | Where-Object { $_.status -eq 'matched' }).Count
    $script:report.evidence_threshold.matched_count = $matchedCount
    $script:report.evidence_threshold.unknown_count = 30 - $matchedCount
    $script:report.evidence_threshold.threshold_met = $allowedManifestStatus -and $matchedCount -ge 27

    if ($allowedManifestStatus) { Invoke-AgentUiSmoke }
    if (-not $allowedManifestStatus) {
        $script:report.status = 'draft_not_scored'
        $script:report.errors += [ordered]@{ code = 'questions_not_approved'; message = 'Draft-manifest results are exploratory and cannot satisfy the acceptance threshold.' }
        Write-Report
        [Console]::Error.WriteLine('Source acceptance evidence was collected for a draft question set; it is not scored for release acceptance.')
        exit 3
    }
    if (-not $script:report.evidence_threshold.threshold_met) {
        $script:report.status = 'failed'
        $script:report.errors += [ordered]@{ code = 'top10_threshold_not_met'; message = 'Fewer than 27 of 30 questions had authorized, exact-path/line/hash evidence in the selected top 10.' }
        Write-Report
        [Console]::Error.WriteLine('Source acceptance finished below the 27/30 top-10 evidence threshold; see the bounded report.')
        exit 2
    }
    $script:report.status = 'completed'
    Write-Report
    Write-Output ("Source acceptance completed: matched={0}/30; snapshot={1}; report={2}" -f $matchedCount, $snapshotId, [System.IO.Path]::GetFullPath($ReportPath))
    exit 0
} catch {
    $message = [string]$_.Exception.Message
    $code = 'runner_failed'
    if ($message -match '^ACCEPTANCE_FAILURE\|([^|]+)\|') { $code = $Matches[1] }
    $script:report.status = 'failed'
    $script:report.errors += [ordered]@{ code = $code; message = 'The acceptance run stopped safely; sensitive response content and credentials were suppressed.' }
    try { Write-Report } catch { }
    [Console]::Error.WriteLine("Source acceptance failed: $code; see the bounded report.")
    exit 1
} finally {
    if ($script:httpClient) {
        $script:httpClient.Dispose()
        $script:httpClient = $null
    }
    $script:accessToken = $null
    $script:requestHeaders = $null
}
