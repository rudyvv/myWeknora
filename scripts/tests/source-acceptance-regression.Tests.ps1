$scriptUnderTest = Join-Path $PSScriptRoot '..\source-acceptance-regression.ps1'

Describe 'source-acceptance-regression plan mode' {
    It 'returns the selected real Agent preset gate as not_run with a fixed target' {
        $json = (& $scriptUnderTest -Scenario agent-presets -PlanOnly | Out-String)
        $report = $json | ConvertFrom-Json

        $report.schema_version | Should Be 1
        $report.runner | Should Be 'go test -json'
        $report.status | Should Be 'not_run'
        $report.automated_status | Should Be 'not_run'
        $report.target.package | Should Be './internal/application/service'
        $report.target.tags | Should Be 'integration'
        $report.target.max_go_test_minutes | Should Be 20
        $report.scenarios.Count | Should Be 1
        $report.scenarios[0].id | Should Be 'agent-presets'
        $report.scenarios[0].status | Should Be 'not_run'
        $report.scenarios[0].tests.Count | Should Be 1
        $report.scenarios[0].tests[0].name | Should Be 'TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope'
        $report.scenarios[0].tests[0].evidence_path | Should Be 'internal/application/service/source_wiki_integration_test.go'
        $report.scenarios[0].field_followup | Should Match 'live model'
        $report.field_followups.Count | Should Be 3
        @($report.field_followups | Where-Object { $_.status -ne 'not_run' }).Count | Should Be 0
    }

    It 'expands all to the closed matrix and never labels plan-only rows passed' {
        $json = (& $scriptUnderTest -Scenario all -PlanOnly | Out-String)
        $report = $json | ConvertFrom-Json

        $report.scenarios.Count | Should Be 10
        @($report.scenarios | Where-Object { $_.status -ne 'not_run' }).Count | Should Be 0
    }

    It 'does not expose an ambient source-test DSN in its safe report' {
        $previous = [Environment]::GetEnvironmentVariable('SOURCE_TEST_POSTGRES_DSN', 'Process')
        try {
            $env:SOURCE_TEST_POSTGRES_DSN = 'T22_SECRET_SENTINEL'
            $json = (& $scriptUnderTest -Scenario source-changes -PlanOnly | Out-String)
            $json.Contains('T22_SECRET_SENTINEL') | Should Be $false
        }
        finally {
            if ($null -eq $previous) {
                Remove-Item Env:\SOURCE_TEST_POSTGRES_DSN -ErrorAction SilentlyContinue
            }
            else {
                $env:SOURCE_TEST_POSTGRES_DSN = $previous
            }
        }
    }

    It 'rejects arbitrary scenario and runner selection' {
        $rejected = $false
        try {
            & $scriptUnderTest -Scenario arbitrary -PlanOnly | Out-Null
        }
        catch {
            $rejected = $true
        }
        $rejected | Should Be $true

        $runnerRejected = $false
        try {
            & $scriptUnderTest -Scenario source-changes -Runner powershell -PlanOnly | Out-Null
        }
        catch {
            $runnerRejected = $true
        }
        $runnerRejected | Should Be $true

        $duplicatesRejected = $false
        try {
            & $scriptUnderTest -Scenario @('source-changes', 'source-changes') -PlanOnly | Out-Null
        }
        catch {
            $duplicatesRejected = $true
        }
        $duplicatesRejected | Should Be $true
    }
}
