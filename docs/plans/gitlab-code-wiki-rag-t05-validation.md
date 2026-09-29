# T05 validation: Git failure reconciliation

Branch: `codex/source-failure-reconciliation`

Base: `3adaa587d16651a6d48f123d73c1e5cceaf75271`

## Implemented behavior

- Source-mode runs load the current published snapshot before resolving the GitLab branch. A branch, token, or Git transport failure therefore records the retained published snapshot ID/SHA and last successful publication time in the failed run result.
- A resolved HEAD is recorded as both the detected commit and fixed processing target. Publication still happens only after the complete fixed-SHA manifest, parsing, and both indexes are ready.
- Failed runs never change the publication pointer. The existing complete-manifest source path remains the deletion truth for non-ancestor/force-push targets; compare or webhook diffs are not used by source-mode publication.
- The source run UI now separates detected HEAD, processing target, current published SHA, last successful publication, and failure state. Recovery remains an explicit manual sync.

## Acceptance evidence

| Acceptance item | Evidence | Status |
| --- | --- | --- |
| Non-ancestor/old-reference/compare failure reconciles with deletion | `TestSourceForcePushReconcilesAgainstTheCompleteManifest`; local Git orphan history is served through the controlled GitLab transport. | Added; PG execution blocked by fixture credentials in this session. |
| Branch/token/fetch failure retains current publication | `TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess`; controlled branch 404, token 401, and Git transport 502. | Added; PG execution blocked by fixture credentials in this session. |
| UI distinguishes failure, current publication, target, and last success | `frontend/src/components/SourceSnapshotRunView.test.ts` failure-state case. | Added; frontend runtime test unavailable because this checkout has no `node_modules/tsx`. |
| Partial work cannot publish or infer deletion | Existing T04/T05-adjacent source integration coverage for parser/vector/keyword failure retains old publication; source-mode `ReadGit` returns no complete manifest on fetch/scan error and publication requires complete member/index counts. | Code path and existing coverage retained; full PG execution pending. |
| Public sync/query behavior with real Git and controlled GitLab errors | New source integration tests use local Git object history, controlled GitLab HTTP, and real parser/model/search boundaries. | Pending approved fixture DSN. |

## Commands

Green:

- `go test ./internal/datasource/connector/gitlab -run 'TestConnector|TestFetch|TestTree|TestProjectPath|TestRaw|TestGitlabFilePath|TestIsSupported' -count=1` — PASS.
- `go test ./internal/application/service -run 'TestManualSyncDoesNotSendSourceModeThroughDocumentIngestion' -count=1 -v` with a worktree-local `GOCACHE` — PASS.
- `gofmt -w internal/types/source_snapshot.go internal/application/service/datasource_source_sync.go internal/application/service/datasource_source_integration_test.go internal/application/service/source_incremental_integration_test.go` — PASS.
- `git diff --check` — PASS.

Red / environment failures:

- First Go attempts without a worktree-local cache failed because the Windows user Go cache was access-denied; rerunning with `GOCACHE` inside this worktree passed the targeted packages.
- `go test ./internal/application/service -tags=integration -run 'TestSourceForcePushReconcilesAgainstTheCompleteManifest|TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess' -count=1 -v` compiled and started, but the shared PostgreSQL fixture rejected the non-secret fallback DSN with password authentication failure. No container was restarted or changed.
- `node --import tsx --test frontend/src/components/SourceSnapshotRunView.test.ts` could not start because `node_modules/tsx` is absent. No dependency installation was attempted.

## Scope and limits

No migration was needed; status fields used by the failed-run result are JSON-only run fields and are not persisted on immutable snapshot rows. No T06 durable trigger, T20 webhook, parser language, MyBatis, global publication manifest, or parent Spec state was changed. Full repository suites remain the responsibility of the integration coordinator.
