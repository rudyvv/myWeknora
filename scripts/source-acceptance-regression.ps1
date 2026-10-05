[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet(
        'all',
        'source-changes',
        'write-fault-isolation',
        'remote-failures',
        'index-atomicity',
        'trigger-delivery',
        'crash-recovery',
        'wiki-cas',
        'retention-cleanup',
        'mixed-scope',
        'agent-presets'
    )]
    [string[]]$Scenario,

    [switch]$PlanOnly,

    [ValidateRange(1, 1320)]
    [int]$ProcessTimeoutSeconds = 1320
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$packageTarget = './internal/application/service'
$goTestTimeoutMinutes = 20
$processTimeoutMinutes = 22
$maximumStdoutBytes = 1048576
$maximumJsonLineBytes = 16384

function New-T22Gate([string]$Name, [string]$EvidencePath, [string[]]$RequiredSubtests = @()) {
    [pscustomobject]@{
        name = $Name
        evidence_path = $EvidencePath
        required_subtests = @($RequiredSubtests)
    }
}

$matrix = @(
    [pscustomobject]@{
        id = 'source-changes'
        title = 'Complete manifests: add, change, delete, rename, and force-push'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceIncrementalCompleteManifestKeepsUniqueRenameAndAccountsForOtherChanges' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceForcePushReconcilesAgainstTheCompleteManifest' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceAmbiguousContentRenameIsExplicitDeleteAndAdd' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceRecreatedOldPathDoesNotStealRenamedFileIdentity' 'internal/application/service/source_incremental_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'write-fault-isolation'
        title = 'Injected SQLSTATE 53100 isolates source file-version write failure and retry'
        field_followup = 'This is a schema-local database fault injection; it does not verify an actually full storage volume.'
        tests = @(
            (New-T22Gate 'TestSourceResourceWriteFaultKeepsPublishedResourcesReadableAndRetries' 'internal/application/service/source_resource_fault_review_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'remote-failures'
        title = 'Branch and authentication failures retain the last published snapshot'
        field_followup = 'The GitLab endpoint, certificate chain, and deployed credentials still require environment-specific field validation.'
        tests = @(
            (New-T22Gate 'TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestGitLabWebhookErrorSourceAcceptsPushAndStartupReconcilesLatestHead' 'internal/application/service/datasource_gitlab_webhook_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'index-atomicity'
        title = 'Vector and keyword index faults never publish a partial snapshot'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceUpdateKeepsPublishedVersionDuringParsingAndVectorFailure' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceUpdateKeywordFailureRetainsPreviousCompletePublication' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceKeywordIndexFailureNeverPublishesFirstSnapshot' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceInvalidEmbeddingNeverPublishes' 'internal/application/service/datasource_source_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'trigger-delivery'
        title = 'Push deduplication, notification idempotency, and continued schedules'
        field_followup = 'Real GitLab webhook delivery and deployed scheduler behavior remain separate field checks.'
        tests = @(
            (New-T22Gate 'TestGitLabPushHookHTTPDurableTriggerDedupAndScheduledReconciliation' 'internal/application/service/datasource_gitlab_webhook_integration_test.go')
            (New-T22Gate 'TestGitLabWebhookRegisteredCronContinuesAfterSourceEntersError' 'internal/application/service/datasource_gitlab_webhook_integration_test.go')
            (New-T22Gate 'TestSourceDuplicateSuccessfulDeliveryDoesNotCreateOrReplacePublication' 'internal/application/service/source_incremental_integration_test.go')
            (New-T22Gate 'TestSourceWikiSameGenerationNoOpDoesNotRequeueAcknowledgedDelivery' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceWikiNotificationSurvivesCredentialRotationAndSameTargetSync' 'internal/application/service/datasource_source_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'crash-recovery'
        title = 'Source and Wiki work recovers from lost workers and expired leases'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceSchedulerRecoversPendingManualTriggerAfterRestart' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceRetryReusesCompletedParseStage' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceLeaseSerializesWorkersAndFencesExpiredOwner' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceWikiRecoveryStartFindsCrashBeforeAttemptIDWasReturned' 'internal/application/service/source_wiki_attempt_recovery_integration_test.go')
            (New-T22Gate 'TestSourceWikiRecoveryStartExpiresUnreturnedCrashAndReleasesOwner' 'internal/application/service/source_wiki_attempt_recovery_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'wiki-cas'
        title = 'Late Wiki jobs and page-version conflicts cannot overwrite current content'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceWikiRunnerDiscardsLateResultAfterLeaseIsLost' 'internal/application/service/source_wiki_attempt_runner_integration_test.go')
            (New-T22Gate 'TestSourceWikiBatchPageConflictRebasesWithinOriginalAttemptAndRevalidates' 'internal/application/service/source_wiki_batch_integration_test.go')
            (New-T22Gate 'TestSourceWikiGenerationPublicationGatesPreserveExistingBody' 'internal/application/service/source_wiki_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'retention-cleanup'
        title = 'Historical evidence, revision GC, source cleanup, and clear fencing'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceWikiRevisionLeasePinOutlivesPruneAndGCDropsOnlyOldIndex' 'internal/application/service/source_wiki_integration_test.go')
            (New-T22Gate 'TestSourceWikiGCRequeuesWhenFinalRevisionOwnerReleasesDuringCollection' 'internal/application/service/source_wiki_integration_test.go')
            (New-T22Gate 'TestSourceWikiRevisionOwnersFollow50And200PostgresWindows' 'internal/application/service/source_wiki_integration_test.go')
            (New-T22Gate 'TestSourceCleanupWorkerRetriesAndWithdrawsOnlySelectedSource' 'internal/application/service/source_cleanup_worker_integration_test.go')
            (New-T22Gate 'TestSourceLifecycleClearRevokesOnlySelectedSourceFromExistingReads' 'internal/application/service/source_lifecycle_review_integration_test.go')
            (New-T22Gate 'TestSourceWikiHistoricalFilteredFileRetainsScopeAndClearWins' 'internal/application/service/source_wiki_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'mixed-scope'
        title = 'Multiple repositories and ordinary documents retain strict read scope'
        field_followup = ''
        tests = @(
            (New-T22Gate 'TestSourceSearchSeparatesSamePathAcrossRepositorySources' 'internal/application/service/datasource_source_integration_test.go')
            (New-T22Gate 'TestSourceAndOrdinaryDocumentsMixWithoutWideningRepositoryPrompt' 'internal/application/service/source_read_integration_test.go')
            (New-T22Gate 'TestSourceWikiMixedDocumentContributionsRequireWholeScopeAndOrdinaryWikiKeepsUnion' 'internal/application/service/source_wiki_integration_test.go')
            (New-T22Gate 'TestQuestionnaireBusinessFlowRelationsStayOnPublishedSnapshotAndAuthorizedAgentScope' 'internal/application/service/datasource_source_integration_test.go')
        )
    }
    [pscustomobject]@{
        id = 'agent-presets'
        title = 'Original RAG, Wiki, and hybrid Agent presets use their authorized public tools'
        field_followup = 'The fixture uses a controlled model; live model behavior and the human-approved representative question corpus still require field validation.'
        tests = @(
            (New-T22Gate 'TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope' 'internal/application/service/source_wiki_integration_test.go' @('rag-qa', 'wiki-qa', 'hybrid-rag-wiki'))
        )
    }
)

if ($Scenario -contains 'all') {
    if ($Scenario.Count -ne 1) {
        throw "Scenario 'all' cannot be combined with individual scenarios."
    }
    $selectedCases = @($matrix)
}
else {
    if (@($Scenario | Select-Object -Unique).Count -ne $Scenario.Count) {
        throw 'Duplicate scenario IDs are not accepted.'
    }
    $selectedCases = @(
        foreach ($scenarioId in $Scenario) {
            $matrix | Where-Object { $_.id -eq $scenarioId }
        }
    )
}

$target = [pscustomobject]@{
    package = $packageTarget
    tags = 'integration'
    count = 1
    max_go_test_minutes = $goTestTimeoutMinutes
    max_process_minutes = $processTimeoutMinutes
    process_timeout_seconds_effective = $ProcessTimeoutSeconds
    max_stdout_bytes = $maximumStdoutBytes
    max_json_line_bytes = $maximumJsonLineBytes
}
$fieldFollowups = @(
    [pscustomobject]@{
        id = 'live-gitlab-transport'
        status = 'not_run'
        evidence_path = 'docs/acceptance/source-fault-agent-matrix.md#live-environment-follow-ups'
        requirement = 'Validate the deployed GitLab endpoint, CA chain, credentials, webhook delivery, and scheduled reconciliation in the approved environment.'
    }
    [pscustomobject]@{
        id = 'human-approved-question-set'
        status = 'not_run'
        evidence_path = 'docs/acceptance/source-representative-questions.json (T06 deliverable; human confirmation required)'
        requirement = 'Use exactly the approved representative questions and verified source evidence; do not prefill retrieval results.'
    }
    [pscustomobject]@{
        id = 'live-model-and-scale'
        status = 'not_run'
        evidence_path = 'scripts/source-acceptance-run.ps1 (T09 deliverable; field execution required)'
        requirement = 'Record actual model, tokenizer, limits, source scope, 1/10/100-file measurements, and unknowns from the approved running environment.'
    }
)

function New-ScenarioResult($Case, [bool]$Plan) {
    $tests = @(
        foreach ($gate in $Case.tests) {
            [pscustomobject]@{
                name = $gate.name
                status = 'not_run'
                evidence_path = $gate.evidence_path
                subtests = @(
                    foreach ($subtestName in $gate.required_subtests) {
                        [pscustomobject]@{
                            name = $subtestName
                            status = 'not_run'
                        }
                    }
                )
            }
        }
    )
    [pscustomobject]@{
        id = $Case.id
        title = $Case.title
        status = 'not_run'
        reason = $(if ($Plan) { 'plan_only' } else { 'not_started' })
        exit_code = $null
        elapsed_ms = 0
        tests = $tests
        field_followup = $Case.field_followup
    }
}

function Stop-T22OwnedProcess([System.Diagnostics.Process]$Process) {
    try {
        if (-not $Process.HasExited) { $Process.Kill($true) }
    }
    catch { }
    try { $null = $Process.WaitForExit(5000) } catch { }
    try {
        if (-not $Process.HasExited) { $Process.Kill($true) }
    }
    catch { }
}

function Add-T22TerminalEvent(
    [byte[]]$Buffer,
    [int]$Length,
    $Actions,
    [string[]]$ExpectedNames
) {
    if ($Length -le 0) { return }
    $line = [System.Text.Encoding]::UTF8.GetString($Buffer, 0, $Length).TrimEnd("`r")
    if ([string]::IsNullOrWhiteSpace($line)) { return }
    try {
        $event = ConvertFrom-Json -InputObject $line -ErrorAction Stop
    }
    catch {
        return
    }
    if ($null -eq $event -or $null -eq $event.PSObject) { return }

    # Go package events legitimately omit Test; inspect properties before reading.
    $testProperty = $event.PSObject.Properties['Test']
    $actionProperty = $event.PSObject.Properties['Action']
    if ($null -eq $testProperty -or $null -eq $actionProperty) { return }
    $testName = [string]$testProperty.Value
    $action = [string]$actionProperty.Value
    if ([string]::IsNullOrEmpty($testName) -or
        $action -notin @('pass', 'fail', 'skip') -or
        $ExpectedNames -cnotcontains $testName) {
        return
    }
    $Actions[$testName] = $action
}

$startedAt = [DateTimeOffset]::UtcNow
$scenarioResults = [System.Collections.Generic.List[object]]::new()

foreach ($case in $selectedCases) {
    $scenarioResult = New-ScenarioResult $case $PlanOnly.IsPresent
    if (-not $PlanOnly) {
        $expectedNames = @($case.tests | ForEach-Object { $_.name })
        $expectedEventNames = [System.Collections.Generic.List[string]]::new()
        foreach ($gate in $case.tests) {
            $null = $expectedEventNames.Add($gate.name)
            foreach ($subtestName in $gate.required_subtests) {
                $null = $expectedEventNames.Add("$($gate.name)/$subtestName")
            }
        }
        $runExpression = '^(' + ($expectedNames -join '|') + ')$'
        $arguments = @(
            'test',
            '-json',
            '-tags=integration',
            '-count=1',
            "-timeout=$($goTestTimeoutMinutes)m",
            '-run',
            $runExpression,
            $packageTarget
        )

        $process = [System.Diagnostics.Process]::new()
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
        $startInfo.FileName = 'go'
        $startInfo.WorkingDirectory = $repositoryRoot
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        foreach ($argument in $arguments) {
            $startInfo.ArgumentList.Add([string]$argument)
        }
        $process.StartInfo = $startInfo

        $timer = [System.Diagnostics.Stopwatch]::StartNew()
        $exitCode = $null
        $timedOut = $false
        $outputLimitExceeded = $false
        $startFailed = $false
        $executionError = $false
        $processStarted = $false
        $testActions = [System.Collections.Generic.Dictionary[string, string]]::new([System.StringComparer]::Ordinal)
        $totalOutputBytes = 0
        $lineBuffer = [byte[]]::new($maximumJsonLineBytes)
        $lineLength = 0
        $readBuffer = [byte[]]::new(4096)
        try {
            $processStarted = $process.Start()
            if (-not $processStarted) {
                $startFailed = $true
            }
            else {
                $stderrTask = $process.StandardError.BaseStream.CopyToAsync([System.IO.Stream]::Null)
                $stdoutStream = $process.StandardOutput.BaseStream
                $processLimit = [TimeSpan]::FromSeconds($ProcessTimeoutSeconds)

                while ($true) {
                    if ($timer.Elapsed -ge $processLimit) {
                        $timedOut = $true
                        Stop-T22OwnedProcess $process
                        break
                    }
                    $remainingWithSentinel = $maximumStdoutBytes - $totalOutputBytes + 1
                    $readSize = [Math]::Min($readBuffer.Length, $remainingWithSentinel)
                    $readTask = $stdoutStream.ReadAsync($readBuffer, 0, $readSize)
                    while (-not $readTask.Wait(100)) {
                        if ($timer.Elapsed -ge $processLimit) {
                            $timedOut = $true
                            Stop-T22OwnedProcess $process
                            break
                        }
                    }
                    if ($timedOut) { break }

                    $bytesRead = $readTask.GetAwaiter().GetResult()
                    if ($bytesRead -eq 0) {
                        if ($lineLength -gt 0) {
                            Add-T22TerminalEvent $lineBuffer $lineLength $testActions $expectedEventNames.ToArray()
                            $lineLength = 0
                        }
                        break
                    }
                    $totalOutputBytes += $bytesRead
                    if ($totalOutputBytes -gt $maximumStdoutBytes) {
                        $outputLimitExceeded = $true
                        Stop-T22OwnedProcess $process
                        break
                    }

                    for ($index = 0; $index -lt $bytesRead; $index++) {
                        $byte = $readBuffer[$index]
                        if ($byte -eq 10) {
                            Add-T22TerminalEvent $lineBuffer $lineLength $testActions $expectedEventNames.ToArray()
                            $lineLength = 0
                            continue
                        }
                        if ($lineLength -ge $maximumJsonLineBytes) {
                            $outputLimitExceeded = $true
                            Stop-T22OwnedProcess $process
                            break
                        }
                        $lineBuffer[$lineLength] = $byte
                        $lineLength++
                    }
                    if ($outputLimitExceeded) { break }
                }

                while (-not $timedOut -and -not $outputLimitExceeded -and -not $process.HasExited) {
                    if ($timer.Elapsed -ge $processLimit) {
                        $timedOut = $true
                        Stop-T22OwnedProcess $process
                        break
                    }
                    $null = $process.WaitForExit(100)
                }
                if (-not $timedOut -and -not $outputLimitExceeded) {
                    while (-not $stderrTask.IsCompleted) {
                        if ($timer.Elapsed -ge $processLimit) {
                            $timedOut = $true
                            Stop-T22OwnedProcess $process
                            break
                        }
                        $null = $stderrTask.Wait(100)
                    }
                    if ($stderrTask.IsCompleted -and $stderrTask.IsFaulted) { $executionError = $true }
                }
                if ($process.HasExited) { $exitCode = $process.ExitCode }
            }
        }
        catch {
            if ($processStarted) {
                $executionError = $true
                Stop-T22OwnedProcess $process
            }
            else {
                $startFailed = $true
            }
        }
        finally {
            $timer.Stop()
            if ($processStarted -and -not $process.HasExited) { Stop-T22OwnedProcess $process }
            $process.Dispose()
        }

        $testResults = @(
            foreach ($gate in $case.tests) {
                $action = $null
                if ($testActions.ContainsKey($gate.name)) { $action = $testActions[$gate.name] }
                $status = switch ($action) {
                    'pass' { 'passed'; break }
                    'fail' { 'failed'; break }
                    'skip' { 'skipped'; break }
                    default { 'not_run' }
                }
                $subtestResults = @(
                    foreach ($subtestName in $gate.required_subtests) {
                        $subtestFullName = "$($gate.name)/$subtestName"
                        $subtestAction = $null
                        if ($testActions.ContainsKey($subtestFullName)) { $subtestAction = $testActions[$subtestFullName] }
                        $subtestStatus = switch ($subtestAction) {
                            'pass' { 'passed'; break }
                            'fail' { 'failed'; break }
                            'skip' { 'skipped'; break }
                            default { 'not_run' }
                        }
                        [pscustomobject]@{ name = $subtestName; status = $subtestStatus }
                    }
                )
                [pscustomobject]@{
                    name = $gate.name
                    status = $status
                    evidence_path = $gate.evidence_path
                    subtests = $subtestResults
                }
            }
        )
        $scenarioResult.tests = $testResults
        $scenarioResult.exit_code = $exitCode
        $scenarioResult.elapsed_ms = [int]$timer.ElapsedMilliseconds

        $allResults = [System.Collections.Generic.List[object]]::new()
        foreach ($testResult in $testResults) {
            $null = $allResults.Add($testResult)
            foreach ($subtestResult in $testResult.subtests) { $null = $allResults.Add($subtestResult) }
        }
        $failedTests = @($allResults | Where-Object { $_.status -in @('failed', 'skipped') })
        $unrunTests = @($allResults | Where-Object { $_.status -eq 'not_run' })
        if ($timedOut) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = 'process_timeout'
        }
        elseif ($outputLimitExceeded) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = 'output_limit_exceeded'
        }
        elseif ($startFailed) {
            $scenarioResult.status = 'not_run'
            $scenarioResult.reason = 'runner_start_failed'
        }
        elseif ($executionError -or $exitCode -ne 0 -or $failedTests.Count -gt 0) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = if ($failedTests.Count -gt 0) { 'test_failed_or_skipped' } else { 'runner_or_test_failed' }
        }
        elseif ($unrunTests.Count -eq $allResults.Count) {
            $scenarioResult.status = 'not_run'
            $scenarioResult.reason = 'expected_tests_not_observed'
        }
        elseif ($unrunTests.Count -gt 0) {
            $scenarioResult.status = 'not_run'
            $scenarioResult.reason = 'incomplete_expected_test_events'
        }
        else {
            $scenarioResult.status = 'passed'
            $scenarioResult.reason = 'all_expected_test_functions_passed'
        }
    }
    $null = $scenarioResults.Add($scenarioResult)
}

$overallStatus = 'not_run'
$automatedStatus = 'not_run'
if (-not $PlanOnly) {
    $failedScenarioCount = @($scenarioResults | Where-Object { $_.status -eq 'failed' }).Count
    $passedScenarioCount = @($scenarioResults | Where-Object { $_.status -eq 'passed' }).Count
    if ($failedScenarioCount -gt 0) {
        $automatedStatus = 'failed'
    }
    elseif ($selectedCases.Count -eq $matrix.Count -and $passedScenarioCount -eq $matrix.Count) {
        $automatedStatus = 'passed'
    }
    # The full T22 result remains not_run until separate field follow-ups are
    # evidenced; this runner cannot turn fixture-only passes into acceptance.
    if ($automatedStatus -eq 'failed') { $overallStatus = 'failed' }
}

$report = [pscustomobject]@{
    schema_version = 1
    runner = 'go test -json'
    status = $overallStatus
    automated_status = $automatedStatus
    plan_only = $PlanOnly.IsPresent
    started_utc = $startedAt.ToString('o')
    finished_utc = [DateTimeOffset]::UtcNow.ToString('o')
    target = $target
    output_policy = 'Raw child stdout and stderr are suppressed; only structured test status events are reported.'
    scenarios = @($scenarioResults.ToArray())
    field_followups = $fieldFollowups
}

Write-Output ($report | ConvertTo-Json -Depth 8 -Compress)

if (-not $PlanOnly) {
    if ($automatedStatus -eq 'failed') { exit 1 }
    if ($automatedStatus -eq 'not_run') { exit 2 }
}
