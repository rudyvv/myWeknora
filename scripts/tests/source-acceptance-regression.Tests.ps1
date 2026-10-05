$scriptUnderTest = Join-Path $PSScriptRoot '..\source-acceptance-regression.ps1'
$script:fakeGoRoot = $null

function Get-FakeGoRoot {
    if ($script:fakeGoRoot) { return $script:fakeGoRoot }

    $script:fakeGoRoot = Join-Path ([IO.Path]::GetTempPath()) ('source-acceptance-regression-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:fakeGoRoot | Out-Null
    $sourcePath = Join-Path $script:fakeGoRoot 'main.go'
    $executablePath = Join-Path $script:fakeGoRoot 'go.exe'
    $fakeGoSource = @'
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const agentTest = "TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope"
const writeFaultTest = "TestSourceResourceWriteFaultKeepsPublishedResourcesReadableAndRetries"
var modes = []string{"rag-qa", "wiki-qa", "hybrid-rag-wiki"}

func emit(action, test string) {
	event := map[string]string{"Action": action, "Package": "fixture/package"}
	if test != "" { event["Test"] = test }
	encoded, _ := json.Marshal(event)
	_, _ = os.Stdout.Write(append(encoded, '\n'))
}

func main() {
	switch os.Getenv("T22_FAKE_GO_MODE") {
	case "package-event-pass":
		emit("start", "")
		emit("run", writeFaultTest)
		emit("pass", writeFaultTest)
		emit("pass", "")
	case "agent-child-skip":
		emit("run", agentTest)
		for _, mode := range modes {
			name := agentTest + "/" + mode
			emit("run", name)
			if mode == "rag-qa" { emit("skip", name) } else { emit("pass", name) }
		}
		emit("pass", agentTest)
	case "agent-all-pass":
		emit("run", agentTest)
		for _, mode := range modes { emit("run", agentTest+"/"+mode); emit("pass", agentTest+"/"+mode) }
		emit("pass", agentTest)
	case "agent-missing":
		emit("run", agentTest)
		for _, mode := range modes[:2] { emit("run", agentTest+"/"+mode); emit("pass", agentTest+"/"+mode) }
		emit("pass", agentTest)
	case "invalid-json-only":
		fmt.Println(`{"Action":"pass","Test":"` + agentTest + `"`)
		emit("pass", "")
	case "oversized-line":
		fmt.Println(strings.Repeat("T22_OUTPUT_SECRET", 1100))
	case "oversized-total":
		for i := 0; i < 110; i++ { fmt.Println(strings.Repeat("x", 10000)) }
	case "timeout-parent":
		if os.Getenv("T22_FAKE_GO_MODE_CHILD") == "1" {
			_ = os.WriteFile(os.Getenv("T22_FAKE_CHILD_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0600)
			time.Sleep(30 * time.Minute)
			return
		}
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "T22_FAKE_GO_MODE_CHILD=1")
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil { os.Exit(41) }
		_ = os.WriteFile(os.Getenv("T22_FAKE_CHILD_PID_FILE"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
		time.Sleep(30 * time.Minute)
	case "timeout-child":
		_ = os.WriteFile(os.Getenv("T22_FAKE_CHILD_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0600)
		time.Sleep(30 * time.Minute)
	}
}
'@
    [IO.File]::WriteAllText($sourcePath, $fakeGoSource, [Text.UTF8Encoding]::new($false))

    $go = (Get-Command go.exe -ErrorAction Stop).Source
    $oldModuleMode = $env:GO111MODULE
    try {
        $env:GO111MODULE = 'off'
        & $go build -trimpath -o $executablePath $sourcePath
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the bounded fake go child process.' }
    }
    finally {
        if ($null -eq $oldModuleMode) { Remove-Item Env:\GO111MODULE -ErrorAction SilentlyContinue }
        else { $env:GO111MODULE = $oldModuleMode }
    }
    return $script:fakeGoRoot
}

function Invoke-FakeGoScenario {
    param(
        [string]$Scenario,
        [string]$Mode,
        [int]$ProcessTimeoutSeconds = 5
    )

    $fakeGoRoot = Get-FakeGoRoot
    $childPidPath = Join-Path $fakeGoRoot 'child.pid'
    Remove-Item -LiteralPath $childPidPath -Force -ErrorAction SilentlyContinue
    $powerShellExe = Join-Path $PSHOME 'pwsh.exe'
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $powerShellExe
    $startInfo.WorkingDirectory = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in @(
        '-NoLogo', '-NoProfile', '-File', $scriptUnderTest,
        '-Scenario', $Scenario,
        '-ProcessTimeoutSeconds', [string]$ProcessTimeoutSeconds
    )) { $startInfo.ArgumentList.Add([string]$argument) }
    $startInfo.Environment['PATH'] = $fakeGoRoot + ';' + $env:PATH
    $startInfo.Environment['T22_FAKE_GO_MODE'] = $Mode
    $startInfo.Environment['T22_FAKE_CHILD_PID_FILE'] = $childPidPath

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    if (-not $process.Start()) { throw 'Could not start the acceptance runner test host.' }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(20000)) {
        try { $process.Kill($true) } catch { }
        $process.WaitForExit()
        throw 'The acceptance runner test host exceeded its 20 second safety bound.'
    }
    $stdout = $stdoutTask.GetAwaiter().GetResult()
    $stderr = $stderrTask.GetAwaiter().GetResult()
    $exitCode = $process.ExitCode
    $process.Dispose()
    $report = $null
    if (-not [string]::IsNullOrWhiteSpace($stdout)) { $report = $stdout.Trim() | ConvertFrom-Json }
    [pscustomobject]@{
        exit_code = $exitCode
        report = $report
        stdout = $stdout
        stderr = $stderr
        child_pid_path = $childPidPath
    }
}

function Remove-FakeGoRoot {
    if (-not $script:fakeGoRoot) { return }

    $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $target = [IO.Path]::GetFullPath($script:fakeGoRoot)
    $targetParent = [IO.Path]::GetDirectoryName($target).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $targetName = [IO.Path]::GetFileName($target)
    if (-not [string]::Equals($targetParent, $tempParent, [StringComparison]::OrdinalIgnoreCase) -or
        $targetName -notmatch '^source-acceptance-regression-[0-9a-f]{32}$' -or
        [string]::Equals($target, $tempParent, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing to recursively remove a fake go fixture outside its uniquely named temp child directory.'
    }

    Remove-Item -LiteralPath $target -Recurse -Force
    $script:fakeGoRoot = $null
}

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
        $report.target.max_process_minutes | Should Be 22
        $report.target.max_stdout_bytes | Should Be 1048576
        $report.target.max_json_line_bytes | Should Be 16384
        $report.scenarios.Count | Should Be 1
        $report.scenarios[0].id | Should Be 'agent-presets'
        $report.scenarios[0].status | Should Be 'not_run'
        $report.scenarios[0].tests.Count | Should Be 1
        $report.scenarios[0].tests[0].name | Should Be 'TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope'
        $report.scenarios[0].tests[0].evidence_path | Should Be 'internal/application/service/source_wiki_integration_test.go'
        $report.scenarios[0].tests[0].subtests.Count | Should Be 3
        @($report.scenarios[0].tests[0].subtests | ForEach-Object { $_.name }) -join ',' | Should Be 'rag-qa,wiki-qa,hybrid-rag-wiki'
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

        $timeoutBoundRejected = $false
        try {
            & $scriptUnderTest -Scenario source-changes -ProcessTimeoutSeconds 1321 -PlanOnly | Out-Null
        }
        catch {
            $timeoutBoundRejected = $true
        }
        $timeoutBoundRejected | Should Be $true
    }

    It 'ignores package-level Go events that have no Test property' {
        $result = Invoke-FakeGoScenario -Scenario write-fault-isolation -Mode package-event-pass

        $result.report.scenarios[0].status | Should Be 'passed'
        $result.report.scenarios[0].tests[0].status | Should Be 'passed'
        $result.report.automated_status | Should Be 'not_run'
        $result.exit_code | Should Be 2
    }

    It 'does not count a passing Agent parent when a required mode is skipped' {
        $result = Invoke-FakeGoScenario -Scenario agent-presets -Mode agent-child-skip
        $row = $result.report.scenarios[0]

        $row.tests[0].status | Should Be 'passed'
        $row.tests[0].subtests[0].name | Should Be 'rag-qa'
        $row.tests[0].subtests[0].status | Should Be 'skipped'
        $row.status | Should Be 'failed'
        $row.reason | Should Be 'test_failed_or_skipped'
        $result.report.automated_status | Should Be 'failed'
        $result.exit_code | Should Be 1
    }

    It 'passes Agent coverage only when all three required mode subtests pass' {
        $result = Invoke-FakeGoScenario -Scenario agent-presets -Mode agent-all-pass
        $row = $result.report.scenarios[0]

        $row.status | Should Be 'passed'
        $row.tests[0].subtests.Count | Should Be 3
        @($row.tests[0].subtests | Where-Object { $_.status -ne 'passed' }).Count | Should Be 0
        $result.report.automated_status | Should Be 'not_run'
        $result.report.status | Should Be 'not_run'
        $result.exit_code | Should Be 2
    }

    It 'keeps a missing required mode not_run instead of passing the Agent parent' {
        $result = Invoke-FakeGoScenario -Scenario agent-presets -Mode agent-missing
        $row = $result.report.scenarios[0]

        $row.tests[0].subtests[2].name | Should Be 'hybrid-rag-wiki'
        $row.tests[0].subtests[2].status | Should Be 'not_run'
        $row.status | Should Be 'not_run'
        $row.reason | Should Be 'incomplete_expected_test_events'
        $result.exit_code | Should Be 2
    }

    It 'does not treat malformed JSON with an embedded test name as a test event' {
        $result = Invoke-FakeGoScenario -Scenario agent-presets -Mode invalid-json-only

        $result.report.scenarios[0].tests[0].status | Should Be 'not_run'
        $result.report.scenarios[0].status | Should Be 'not_run'
        $result.report.scenarios[0].reason | Should Be 'expected_tests_not_observed'
        $result.exit_code | Should Be 2
    }

    It 'fails and suppresses output when a single JSON line exceeds its bound' {
        $result = Invoke-FakeGoScenario -Scenario write-fault-isolation -Mode oversized-line

        $result.report.scenarios[0].status | Should Be 'failed'
        $result.report.scenarios[0].reason | Should Be 'output_limit_exceeded'
        ($result.stdout + $result.stderr).Contains('T22_OUTPUT_SECRET') | Should Be $false
        $result.exit_code | Should Be 1
    }

    It 'fails and suppresses output when cumulative stdout exceeds its bound' {
        $result = Invoke-FakeGoScenario -Scenario write-fault-isolation -Mode oversized-total

        $result.report.scenarios[0].status | Should Be 'failed'
        $result.report.scenarios[0].reason | Should Be 'output_limit_exceeded'
        $result.exit_code | Should Be 1
    }

    It 'times out and kills the owned child process tree' {
        $result = Invoke-FakeGoScenario -Scenario write-fault-isolation -Mode timeout-parent -ProcessTimeoutSeconds 2
        $row = $result.report.scenarios[0]
        $pidText = Get-Content -LiteralPath $result.child_pid_path -ErrorAction Stop
        $childPid = [int]$pidText
        $processSurvived = $false
        for ($attempt = 0; $attempt -lt 30; $attempt++) {
            if (Get-Process -Id $childPid -ErrorAction SilentlyContinue) { $processSurvived = $true; Start-Sleep -Milliseconds 100 }
            else { break }
        }

        $row.status | Should Be 'failed'
        $row.reason | Should Be 'process_timeout'
        $processSurvived | Should Be $false
        $result.exit_code | Should Be 1
    }

}

Describe 'source-acceptance-regression fake fixture cleanup' {
    It 'recursively removes only its uniquely named direct temp child' {
        $script:fakeGoRoot = Join-Path ([IO.Path]::GetTempPath()) ('source-acceptance-regression-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $script:fakeGoRoot | Out-Null
        $target = [IO.Path]::GetFullPath($script:fakeGoRoot)
        $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
        [IO.Path]::GetDirectoryName($target).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) | Should Be $tempParent
        [IO.Path]::GetFileName($target) | Should Match '^source-acceptance-regression-[0-9a-f]{32}$'
        Remove-FakeGoRoot
        Test-Path -LiteralPath $target | Should Be $false
    }
}
