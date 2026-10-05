# Source Fault and Agent Regression Matrix

This T10 matrix maps each automated row to existing behavioral integration tests. The runner is [`scripts/source-acceptance-regression.ps1`](../../scripts/source-acceptance-regression.ps1); it invokes only the named Go tests in `./internal/application/service` with the `integration` build tag. It does not start, stop, or configure PostgreSQL, Docker, GitLab, or a model service.

## Matrix

`not_run` is the starting status for every PostgreSQL-backed row. A test name in this document or runner catalog is a gate reference, not evidence that the gate passed. The runner reports `passed` only after observing a Go JSON terminal pass event for every expected top-level test in that row.

| Cell | Existing behavioral gates | Current status | Field or supplement requirement |
| --- | --- | --- | --- |
| `source-changes` | `TestSourceIncrementalCompleteManifestKeepsUniqueRenameAndAccountsForOtherChanges`, `TestSourceForcePushReconcilesAgainstTheCompleteManifest`, `TestSourceAmbiguousContentRenameIsExplicitDeleteAndAdd`, `TestSourceRecreatedOldPathDoesNotStealRenamedFileIdentity` — [`source_incremental_integration_test.go`](../../internal/application/service/source_incremental_integration_test.go) | `not_run` | The fixture gates cover complete-manifest behavior. Representative-repository measurements remain a separate field gate. |
| `write-fault-isolation` | `TestSourceResourceWriteFaultKeepsPublishedResourcesReadableAndRetries` — [`source_resource_fault_review_integration_test.go`](../../internal/application/service/source_resource_fault_review_integration_test.go) | `not_run` | Uses a schema-local trigger to inject SQLSTATE `53100`; it does not simulate a physically full volume. |
| `remote-failures` | `TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess` — [`source_incremental_integration_test.go`](../../internal/application/service/source_incremental_integration_test.go); `TestGitLabWebhookErrorSourceAcceptsPushAndStartupReconcilesLatestHead` — [`datasource_gitlab_webhook_integration_test.go`](../../internal/application/service/datasource_gitlab_webhook_integration_test.go) | `not_run` | Fixture coverage includes missing branch, invalid token, and unavailable Git transport. Deployed GitLab endpoint, CA chain, credentials, and inbound delivery still need field validation. |
| `index-atomicity` | `TestSourceUpdateKeepsPublishedVersionDuringParsingAndVectorFailure`, `TestSourceUpdateKeywordFailureRetainsPreviousCompletePublication` — [`source_incremental_integration_test.go`](../../internal/application/service/source_incremental_integration_test.go); `TestSourceKeywordIndexFailureNeverPublishesFirstSnapshot`, `TestSourceInvalidEmbeddingNeverPublishes` — [`datasource_source_integration_test.go`](../../internal/application/service/datasource_source_integration_test.go) | `not_run` | These gates exercise controlled parser/vector/keyword failure paths. They are not throughput or production-index measurements. |
| `trigger-delivery` | `TestGitLabPushHookHTTPDurableTriggerDedupAndScheduledReconciliation`, `TestGitLabWebhookRegisteredCronContinuesAfterSourceEntersError` — [`datasource_gitlab_webhook_integration_test.go`](../../internal/application/service/datasource_gitlab_webhook_integration_test.go); `TestSourceDuplicateSuccessfulDeliveryDoesNotCreateOrReplacePublication` — [`source_incremental_integration_test.go`](../../internal/application/service/source_incremental_integration_test.go); `TestSourceWikiSameGenerationNoOpDoesNotRequeueAcknowledgedDelivery`, `TestSourceWikiNotificationSurvivesCredentialRotationAndSameTargetSync` — [`datasource_source_integration_test.go`](../../internal/application/service/datasource_source_integration_test.go) | `not_run` | Fixture delivery is not a substitute for validating the deployed GitLab webhook or live scheduler. |
| `crash-recovery` | `TestSourceSchedulerRecoversPendingManualTriggerAfterRestart`, `TestSourceRetryReusesCompletedParseStage`, `TestSourceLeaseSerializesWorkersAndFencesExpiredOwner` — [`datasource_source_integration_test.go`](../../internal/application/service/datasource_source_integration_test.go); `TestSourceWikiRecoveryStartFindsCrashBeforeAttemptIDWasReturned`, `TestSourceWikiRecoveryStartExpiresUnreturnedCrashAndReleasesOwner` — [`source_wiki_attempt_recovery_integration_test.go`](../../internal/application/service/source_wiki_attempt_recovery_integration_test.go) | `not_run` | Retry/checkpoint and expired-lease fixtures cover recovery contracts; they do not claim a live deployment crash drill. |
| `wiki-cas` | `TestSourceWikiRunnerDiscardsLateResultAfterLeaseIsLost` — [`source_wiki_attempt_runner_integration_test.go`](../../internal/application/service/source_wiki_attempt_runner_integration_test.go); `TestSourceWikiBatchPageConflictRebasesWithinOriginalAttemptAndRevalidates` — [`source_wiki_batch_integration_test.go`](../../internal/application/service/source_wiki_batch_integration_test.go); `TestSourceWikiGenerationPublicationGatesPreserveExistingBody` — [`source_wiki_integration_test.go`](../../internal/application/service/source_wiki_integration_test.go) | `not_run` | These are fixture gates for stale worker results, page-version conflicts, and guarded publication. |
| `retention-cleanup` | `TestSourceWikiRevisionLeasePinOutlivesPruneAndGCDropsOnlyOldIndex`, `TestSourceWikiGCRequeuesWhenFinalRevisionOwnerReleasesDuringCollection`, `TestSourceWikiRevisionOwnersFollow50And200PostgresWindows`, `TestSourceWikiHistoricalFilteredFileRetainsScopeAndClearWins` — [`source_wiki_integration_test.go`](../../internal/application/service/source_wiki_integration_test.go); `TestSourceCleanupWorkerRetriesAndWithdrawsOnlySelectedSource` — [`source_cleanup_worker_integration_test.go`](../../internal/application/service/source_cleanup_worker_integration_test.go); `TestSourceLifecycleClearRevokesOnlySelectedSourceFromExistingReads` — [`source_lifecycle_review_integration_test.go`](../../internal/application/service/source_lifecycle_review_integration_test.go) | `not_run` | PostgreSQL-backed integration execution is required; none was run by this T10 executor. |
| `mixed-scope` | `TestSourceSearchSeparatesSamePathAcrossRepositorySources`, `TestQuestionnaireBusinessFlowRelationsStayOnPublishedSnapshotAndAuthorizedAgentScope` — [`datasource_source_integration_test.go`](../../internal/application/service/datasource_source_integration_test.go); `TestSourceAndOrdinaryDocumentsMixWithoutWideningRepositoryPrompt` — [`source_read_integration_test.go`](../../internal/application/service/source_read_integration_test.go); `TestSourceWikiMixedDocumentContributionsRequireWholeScopeAndOrdinaryWikiKeepsUnion` — [`source_wiki_integration_test.go`](../../internal/application/service/source_wiki_integration_test.go) | `not_run` | Controlled fixtures cover source-to-source and source-to-document boundaries. The human-approved representative question set is separate. |
| `agent-presets` | `TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope` — [`source_wiki_integration_test.go`](../../internal/application/service/source_wiki_integration_test.go) | `not_run` | The test exercises the three preset subtests with a controlled model. It does not certify live-model answer quality or the representative corpus. |

## Agent submatrix

All three rows below are exercised by the single integration test listed above. A row becomes `passed` only when the parent test completes successfully; subtest names are retained here to make the intended mode distinction explicit.

| Agent mode | Existing subtest | Current status | Evidence |
| --- | --- | --- | --- |
| RAG | `rag-qa` | `not_run` | [`source_wiki_integration_test.go`](../../internal/application/service/source_wiki_integration_test.go): `TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope` |
| Wiki | `wiki-qa` | `not_run` | Same test; verifies Wiki tools without source grep. |
| Wiki + RAG | `hybrid-rag-wiki` | `not_run` | Same test; verifies both tool families with the question bound to the pinned source scope. |

## Report and execution contract

Run a plan without executing PostgreSQL-backed tests:

```powershell
pwsh -NoLogo -NoProfile -File scripts/source-acceptance-regression.ps1 -Scenario all -PlanOnly
```

Run all ten automated rows against the already prepared integration environment:

```powershell
pwsh -NoLogo -NoProfile -File scripts/source-acceptance-regression.ps1 -Scenario all
```

The runner uses one fixed Go package, the `integration` build tag, `-count=1`, a 20-minute Go test timeout, and a 22-minute per-row process timeout. Callers can select one or more closed-set scenario IDs; arbitrary package, executable, test regex, or runner arguments are not accepted. It inherits the environment required by the existing fixture but never reads or prints DSNs or credential values. It neither starts nor stops shared dependencies.

The JSON report separates each scenario status (`passed`, `failed`, or `not_run`) from the overall T22 status. Only terminal Go test events are retained; child stdout and stderr are suppressed. A package compile success or an unmatched `-run` pattern cannot produce a scenario pass: if expected test events are absent, the row is `not_run` or `failed` as appropriate. A single selected row cannot mark the whole automated matrix passed. Even when all ten automated rows pass, overall status remains `not_run` until field follow-ups are separately evidenced.

`-PlanOnly` reports `not_run` and exits successfully. An executed failed row exits 1; an incomplete selected set or a run that observed no complete test set exits 2. All ten automated rows passing exits 0, while the report's overall T22 `status` remains `not_run` until field follow-ups are separately evidenced. The runner emits only a safe JSON summary; it does not save raw Go output or a report file.

The independent PowerShell planner/report tests are in [`scripts/tests/source-acceptance-regression.Tests.ps1`](../../scripts/tests/source-acceptance-regression.Tests.ps1). Their `PlanOnly` checks do not execute any integration gate.

## Live-environment follow-ups

These are intentionally not automated or marked passed by this matrix runner:

| Follow-up | Status | Evidence required |
| --- | --- | --- |
| GitLab transport, CA, deployed credentials, webhook, and scheduler | `not_run` | Safe operational evidence from the user-approved environment; do not include credentials. |
| Human confirmation of exactly 30 representative questions and their source evidence | `not_run` | T06 deliverable `docs/acceptance/source-representative-questions.json`, with no prefilled retrieval hits. |
| Live model behavior, retrieval evidence, and measured 1/10/100-file workload | `not_run` | T09 field runner `scripts/source-acceptance-run.ps1` plus actual hardware/model/tokenizer/limits and measured outputs; unknown measurements stay `unknown`. |

No new Go integration test was added for T10: the listed behavior gates already exist on the formal base. Any subsequently discovered behavioral gap should be sent to the coordinating root with the concrete scenario and proposed test file before adding an integration test or touching a shared fixture.

## T10 local verification

On 2026-10-05, `Invoke-Pester -Script scripts/tests/source-acceptance-regression.Tests.ps1 -PassThru` passed 4/4 planner/report-contract tests. No PostgreSQL-backed scenario was executed by this workstream, and this Pester result does not change the `not_run` matrix statuses.
