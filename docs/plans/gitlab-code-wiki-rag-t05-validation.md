# T05 validation: Git failure reconciliation

Branch: `codex/source-failure-reconciliation`

Base: `3adaa587d16651a6d48f123d73c1e5cceaf75271`

## Implemented behavior

- Source-mode runs load the current published snapshot before resolving the GitLab branch. A branch, token, or Git transport failure therefore records the retained published snapshot ID/SHA and last successful publication time in the failed run result.
- The publication lookup runs before parser/index readiness checks, so readiness failures also report the retained SHA/time. A JSON-only `publication_checked` flag distinguishes a confirmed empty first publication from a failed/unknown lookup; a missing snapshot repository fails safely without claiming either state.
- A resolved HEAD is recorded as both the detected commit and fixed processing target. Publication still happens only after the complete fixed-SHA manifest, parsing, and both indexes are ready.
- Failed runs never change the publication pointer. The existing complete-manifest source path remains the deletion truth for non-ancestor/force-push targets; compare or webhook diffs are not used by source-mode publication.
- The source run UI now separates detected HEAD, processing target, current published SHA, last successful publication, and failure state. Recovery remains an explicit manual sync.

## Acceptance evidence

| Acceptance item | Evidence | Status |
| --- | --- | --- |
| Non-ancestor/old-reference/compare failure reconciles with deletion | `TestSourceForcePushReconcilesAgainstTheCompleteManifest`; local Git force-push removes the old branch history, then keyword, vector, and hybrid searches are checked against the sole new source member and its snapshot/file-version/SHA. | PASS against the dedicated PostgreSQL test database, including all three search modes. |
| Branch/token/fetch failure retains current publication | `TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess`; controlled branch 404, token 401, and Git transport 502. | PASS against the dedicated PostgreSQL test database. |
| UI distinguishes first failure from a retained publication | `frontend/src/components/SourceSnapshotRunView.test.ts` checks both an unsuccessful later run (including detected/target SHA and last-success state) and a first-ever failure with no retained-publication claim. | PASS. |
| Publication lookup failure is distinct from a confirmed first failure | `publication_checked` is set only after `GetPublished` succeeds; the UI tests all three outcomes: retained version, confirmed no prior version, and unknown publication state. | PASS: 4 component tests total, including the three publication states. |
| Missing snapshot repository fails safely and records the run | `TestProcessSourceSyncRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing`; the full `ProcessSync` variant is also covered by `TestSourceRunRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing`. | Unit and PostgreSQL integration tests PASS. |
| Parser readiness failure retains publication status and old fixed reads | `TestSourceParserReadinessFailureRetainsPublishedStatusAndReadableVersion` verifies failed-run SHA/time, unchanged publication pointer, and old file-version read. | PASS against the dedicated PostgreSQL test database. |
| Partial work cannot publish or infer deletion | `TestSourceUpdateKeepsPublishedVersionDuringParsingAndVectorFailure` and `TestSourceUpdateKeywordFailureRetainsPreviousCompletePublication`; source-mode `ReadGit` returns no complete manifest on fetch/scan error and publication requires complete member/index counts. | Both integration tests PASS against the dedicated PostgreSQL test database. |
| Previously published Wiki evidence remains readable without Git access | `TestSourceWikiEvidenceRemainsReadableAfterForcePushAndGitUnavailable` resolves a saved evidence version after force-push and Git/API transport loss. `TestSourceWikiHistoricalFilteredFileRetainsScopeAndClearWins` covers historical file scope and clear precedence. | Both integration tests PASS against the dedicated PostgreSQL test database. |
| Public sync/query behavior with real Git and controlled GitLab errors | Integration tests use local Git object history, controlled GitLab HTTP, and real parser/model/search boundaries. | Focused PostgreSQL-backed integration set PASS. |

## Commands

Green:

- `go test ./internal/datasource/connector/gitlab -run 'TestConnector|TestFetch|TestTree|TestProjectPath|TestRaw|TestGitlabFilePath|TestIsSupported' -count=1` — PASS.
- `go test ./internal/application/service -run 'TestProcessSourceSyncRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing|TestManualSyncDoesNotSendSourceModeThroughDocumentIngestion' -count=1` with a worktree-local `GOCACHE` — PASS.
- `go test -p 1 ./internal/application/service -tags=integration -run '^$' -count=1` with a worktree-local `GOCACHE` — PASS (integration package compiles; no tests executed).
- `go test -p 1 ./internal/application/service -tags=integration -run '^(TestSourceForcePushReconcilesAgainstTheCompleteManifest|TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess|TestSourceUpdateKeepsPublishedVersionDuringParsingAndVectorFailure|TestSourceUpdateKeywordFailureRetainsPreviousCompletePublication|TestSourceRunRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing|TestSourceWikiEvidenceRemainsReadableAfterForcePushAndGitUnavailable|TestSourceWikiHistoricalFilteredFileRetainsScopeAndClearWins)$' -count=1 -timeout 10m` with the dedicated loopback PostgreSQL test DSN, parser runtime, and parser cache injected via environment — PASS (all selected tests and subtests; 53.855s).
- `go test -p 1 ./internal/application/service -tags=integration -run '^(TestSourceParserReadinessFailureRetainsPublishedStatusAndReadableVersion|TestSourceRunRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing|TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess)$' -count=1 -timeout 10m` with the dedicated loopback PostgreSQL test DSN, parser runtime, and parser cache injected via environment — PASS (all selected tests and subtests).
- `node --import tsx --test src/components/SourceSnapshotRunView.test.ts` from `frontend/`, using an existing read-only dependency tree — PASS (4 tests).
- `gofmt -w internal/types/source_snapshot.go internal/application/service/datasource_source_sync.go internal/application/service/datasource_source_integration_test.go internal/application/service/source_incremental_integration_test.go internal/application/service/datasource_service_test.go` — PASS.
- `git diff --check` — PASS.

Red / environment failures:

- First Go attempts without a worktree-local cache failed because the Windows user Go cache was access-denied; rerunning with `GOCACHE` inside this worktree passed the targeted packages.
- An earlier attempt using the non-secret fallback DSN failed authentication; a request to read credentials from the prior shared container was denied and was not retried. No old-container metadata was read in the final validation. The focused PostgreSQL suite passed using the separately provisioned test database and explicitly supplied test-only DSN; the value is not recorded here.
- The frontend test initially could not resolve dependencies from the repository root; rerunning from `frontend/` with the existing dependency tree passed. No dependency installation or modification was performed.

## Scope and limits

No migration was needed; status fields used by the failed-run result are JSON-only run fields and are not persisted on immutable snapshot rows. No T06 durable trigger, T20 webhook, parser language, MyBatis, global publication manifest, or parent Spec state was changed. Full repository suites remain the responsibility of the integration coordinator.
