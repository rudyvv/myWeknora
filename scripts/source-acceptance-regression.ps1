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

    [switch]$PlanOnly
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$packageTarget = './internal/application/service'
$goTestTimeoutMinutes = 20
$processTimeoutMinutes = 22

function New-T22Gate([string]$Name, [string]$EvidencePath) {
    [pscustomobject]@{
        name = $Name
        evidence_path = $EvidencePath
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
            (New-T22Gate 'TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope' 'internal/application/service/source_wiki_integration_test.go')
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

$startedAt = [DateTimeOffset]::UtcNow
$scenarioResults = [System.Collections.Generic.List[object]]::new()

foreach ($case in $selectedCases) {
    $scenarioResult = New-ScenarioResult $case $PlanOnly.IsPresent
    if (-not $PlanOnly) {
        $expectedNames = @($case.tests | ForEach-Object { $_.name })
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
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        foreach ($argument in $arguments) {
            $startInfo.ArgumentList.Add([string]$argument)
        }
        $process.StartInfo = $startInfo

        $timer = [System.Diagnostics.Stopwatch]::StartNew()
        $exitCode = $null
        $timedOut = $false
        $startFailed = $false
        $executionError = $false
        $processStarted = $false
        $testActions = @{}
        try {
            $processStarted = $process.Start()
            if (-not $processStarted) {
                $startFailed = $true
            }
            else {
                $stdoutTask = $process.StandardOutput.ReadToEndAsync()
                $stderrTask = $process.StandardError.BaseStream.CopyToAsync([System.IO.Stream]::Null)
                $waitMilliseconds = [int]([TimeSpan]::FromMinutes($processTimeoutMinutes).TotalMilliseconds)
                if (-not $process.WaitForExit($waitMilliseconds)) {
                    $timedOut = $true
                    try { $process.Kill($true) } catch { }
                    $process.WaitForExit()
                }
                $stdoutText = $stdoutTask.GetAwaiter().GetResult()
                $null = $stderrTask.GetAwaiter().GetResult()
                $exitCode = $process.ExitCode

                foreach ($line in [regex]::Split($stdoutText, "`r?`n")) {
                    if ([string]::IsNullOrWhiteSpace($line)) { continue }
                    try {
                        $event = ConvertFrom-Json -InputObject $line -ErrorAction Stop
                    }
                    catch {
                        continue
                    }
                    if ($event.Test -and $event.Action -in @('pass', 'fail', 'skip') -and
                        $expectedNames -contains [string]$event.Test) {
                        $testActions[[string]$event.Test] = [string]$event.Action
                    }
                }
                Remove-Variable stdoutText -ErrorAction SilentlyContinue
            }
        }
        catch {
            if ($processStarted) {
                $executionError = $true
            }
            else {
                $startFailed = $true
            }
        }
        finally {
            $timer.Stop()
            $process.Dispose()
        }

        $testResults = @(
            foreach ($gate in $case.tests) {
                $action = $testActions[$gate.name]
                $status = switch ($action) {
                    'pass' { 'passed'; break }
                    'fail' { 'failed'; break }
                    default { 'not_run' }
                }
                [pscustomobject]@{
                    name = $gate.name
                    status = $status
                    evidence_path = $gate.evidence_path
                }
            }
        )
        $scenarioResult.tests = $testResults
        $scenarioResult.exit_code = $exitCode
        $scenarioResult.elapsed_ms = [int]$timer.ElapsedMilliseconds

        $failedTests = @($testResults | Where-Object { $_.status -eq 'failed' })
        $unrunTests = @($testResults | Where-Object { $_.status -eq 'not_run' })
        if ($timedOut) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = 'process_timeout'
        }
        elseif ($startFailed) {
            $scenarioResult.status = 'not_run'
            $scenarioResult.reason = 'runner_start_failed'
        }
        elseif ($executionError -or $exitCode -ne 0 -or $failedTests.Count -gt 0) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = 'runner_or_test_failed'
        }
        elseif ($unrunTests.Count -eq $testResults.Count) {
            $scenarioResult.status = 'not_run'
            $scenarioResult.reason = 'expected_tests_not_observed'
        }
        elseif ($unrunTests.Count -gt 0) {
            $scenarioResult.status = 'failed'
            $scenarioResult.reason = 'incomplete_expected_test_set'
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
