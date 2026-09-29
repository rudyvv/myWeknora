# T06 validation: durable source triggers

Ticket: [GitHub #14](https://github.com/rudyvv/myWeknora/issues/14). Branch: `codex/source-durable-triggers`. Review base: `7f4fd1dcdf48a157976f6e0ebc76d39f5fbea10`. Migration: `000108` (T10 owns `000107`). No issue, parent Spec, or GitHub state was changed.

## Implemented behavior

- Source-mode data sources default to an hourly schedule while retaining an explicit configured schedule. Manual and scheduled source triggers register in PostgreSQL before delivery; enqueue failure leaves the run queued for scheduler reconciliation.
- Per-source state tracks configuration generation, pending latest trigger, active run, expiring owner lease and monotonic fencing token. Duplicate/stale queue deliveries are no-ops; config edits, credential changes, credential clearing and source deletion fence active work before the source mutation.
- While B is active, C is replaced by D as the single pending catch-up run. B can publish; release then dispatches D.
- Source runs persist their fixed target SHA and phase. Existing snapshot members, parse artifacts, staged chunks and embedding artifacts allow retry/restart to reuse completed work. Expired leases are fenced and re-dispatched.
- Snapshot publication, published pointer, last-success timestamp, run phase and a `source.wiki.update` pending outbox row are written in one PostgreSQL transaction.
- The UI polls queued/running runs, permits another source-mode trigger to become the latest catch-up request, distinguishes queued from running, and exposes the last successful publication in source-run details.

## Acceptance evidence and remaining work

| Acceptance item | Evidence | Status |
| --- | --- | --- |
| Hourly default/configurable schedule, durable manual trigger, lease/fencing and config generation | `TestManualSourceSyncRemainsPendingWhenQueueEnqueueFails`; `TestSourceLeaseSerializesWorkersAndFencesExpiredOwner`; scheduler default test. The lease test races 12 claimants, checks one winner, stale-delivery no-op, expiry reclamation with a higher token, stale-owner rejection and config-generation fencing. | PASS |
| B publishes, C/D coalesce to D | `TestSourceManualTriggersSerializeAndCatchUpToLatestCommit` verifies C is canceled, B completes, and D—not C—is the catch-up publication. | PASS |
| Atomic publication plus recoverable Wiki signal | PostgreSQL publication transaction atomically inserts the `source.wiki.update` outbox row; integration tests verify one signal per committed snapshot. No outbox relay/consumer currently claims, delivers or acknowledges those rows. | PARTIAL: durable signal creation is implemented; delivery/recovery after a lost signal remains to be wired. |
| Stage reuse, crash/lease recovery, duplicate delivery and preserved budgets | `TestSourceRetryReusesCompletedParseStage` verifies retry reuses the same snapshot, parsed artifact and file version and does not duplicate its signal. The scheduler restart test verifies queued trigger redelivery. Retry paths leave the persisted budget fields unchanged. | PARTIAL: stage/lease recovery is covered; explicit dropped-outbox-signal recovery and budget accounting assertions remain. |
| UI running/waiting/failure-retry/last success | Settings and sync-log views poll queued/running status; source trigger stays available during a run; source-run details preserve the last successful publication time. | PASS for implemented status display; no separate retry action was added (manual trigger is the retry path). |
| Public trigger plus PostgreSQL concurrency/restart/failure fixtures | Focused real PostgreSQL integration set below covers public `ManualSync`, queue failure, scheduler restart recovery, B/C/D coalescing, lease races/fencing, retry-stage reuse, and publication rejection on embedding/keyword-index failure. | PARTIAL only because outbox delivery/recovery is not implemented. |

## Validation commands

Using the isolated PostgreSQL test database on loopback port 57521, the existing parser Python runtime and prefetched Java grammar cache (no project build, install, or old-container credential access):

```powershell
go test -p 1 ./internal/application/service -tags=integration -run '^(TestSourceLeaseSerializesWorkersAndFencesExpiredOwner|TestSourceRetryReusesCompletedParseStage|TestSourceInvalidEmbeddingNeverPublishes|TestSourceKeywordIndexFailureNeverPublishesFirstSnapshot|TestSourceManualTriggersSerializeAndCatchUpToLatestCommit|TestSourceFirstJavaSnapshotIsPublishedAndSearchable|TestManualSourceSyncRemainsPendingWhenQueueEnqueueFails|TestSourceSchedulerRecoversPendingManualTriggerAfterRestart)$' -count=1 -timeout 10m
```

Result: PASS, all eight selected tests (34.268s).

```powershell
go test -p 1 ./internal/datasource ./internal/application/repository
go test -p 1 ./internal/application/service -run '^(TestDataSourceServiceDeleteSQLiteCleansUpAfterSoftDelete|TestDataSourceServiceDeleteKeepsCleanupStateWhenSoftDeleteFails|TestDeleteKnowledgeBaseCleansUpSQLiteDataSources)$' -count=1
```

Results: PASS. The SQLite delete-test fixture now closes its database handle so Windows can remove its temporary directory.

The frontend app and Node projects type-check independently with the bundled Node runtime and T06-owned temporary build-info files. `npm run type-check` targets `frontend/node_modules/.tmp` and fails with `EPERM` in the shared dependency tree; no shared cache files were removed or modified.

The full `go test -p 1 ./internal/datasource ./internal/application/repository ./internal/application/service` run is not green on this Windows host. The four unrelated service tests `TestCacheBudgetGuardKeepsSmallWipesBig`, `TestCleanImageScratchCommandShape`, `TestSeededSkillHelperIsDirectlyExecutable` and `TestRuntimePrerequisitesResolveSkillLocalCLI` require POSIX `sh` or direct shell-script execution and fail because `sh` is absent / `.sh` is not a Win32 executable. The source-specific PostgreSQL and targeted service tests above pass.

`git diff --check` is run before the checkpoint commit. Full repository suites, outbox relay, issue closure and parent Spec changes remain with the integration coordinator.
