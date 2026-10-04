# T19 whole-ticket review — 335f5769

Fixed base: `e2677b62e8d847d2585f103d634f4945c0839d50`. Frozen clean candidate: `335f5769d844e6ada4348d45278fe78fe07a6cd6`, main execution tree `C:/Users/28211/.codex/worktrees/a8ea/WeKnora`. Commits since base: `9c18ef2b`, `1e69b497`, `db1fca60`, `bc6987cb`, `c42fea97`, `335f5769`. The pure contribution, frontend and auxiliary test slices remain accepted/frozen; whole T19 is not accepted.

## Actual frozen validation

Root collected final PostgreSQL session 29402: all four checked-in gates **PASS**, package **35.702s**. Cleanup failure/exact retry/current-history withdrawal/physical reclaim/source-B isolation passed 9.69s; unbind retains published reads and rejects manual sync 5.72s; clear revokes source A from old scopes while keeping B readable 10.88s; repeated clear preserves active operation/fence 4.93s. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-335f5769-final-pg.jsonl`. This is real execution against the dedicated 57822 test database and original migration 117, not compilation or a substituted fixture schema.

The final pre-freeze cleanup-only repair also passed 12.27s/package 16.840s (session 66258); the earlier JSON encoding and missing SQL argument failures are repaired. These successes do not exercise the concurrent management-write race below. Earlier accepted UI/native evidence is retained in the coordination/UI review documents rather than repeated here.

## Standards

Sol/high review: **1 documented hard violation (P2), 2 non-blocking Fowler judgements**.

- New lifecycle handlers in `internal/handler/datasource.go:539,572,604` serialize `err.Error()` from repository-backed unbind/clear/retry. This breaches the approved public-management contract that SQL errors and internal paths must not enter UI error fields. Return stable public errors and retain diagnostics in server logs.
- Possible Duplicated Code: current-page and revision owner discovery at `source_cleanup_worker.go:316–349` repeat the same four-way SQL shape.
- Possible Data Clumps: withdrawal helper arguments at `source_cleanup_worker.go:462–470` carry related page identity and projection fields separately. These are judgement calls, not acceptance blockers.

## Spec

Sol/high review: **2 findings**, worst **P1**.

- **P1 — A stale public management write can reverse clear.** `UpdateDataSource` copies lifecycle state and stored credentials before persistence (`datasource_service.go:225`); generic repository `Updates(ds)` (`datasource_repo.go:90`) has no lifecycle CAS or current-row recheck. Credentials update and resume likewise persist previously loaded structs. Clear can commit between read and write, then the older operation can restore bound/query-enabled state and stripped credentials. The generation guard runs in a separate transaction and cannot protect the later write. This violates the accepted rule that pending/running/failed/retried cleanup never restores query rights. Lifecycle recheck and persistence must share a lock/CAS; omitting lifecycle columns alone does not protect copied credentials.
- **P2 — Public lifecycle failures expose raw database diagnostics.** Same handler locations as the documented Standards breach. The UI displays the response message, so repository SQL failures can enter public UI. Use stable sanitized error mappings with internal logging.

Root independently inspected both call paths and actually reproduced P1 through all three public service methods with a deterministic wrapped repository Update barrier: clear commits after the service read/validation, before the actual old write. PUT failed 6.17s, credentials update failed 2.78s, resume failed 2.49s; package FAIL 16.100s. Every persisted row returned `query_enabled=true`, `binding_state=bound`, configured credentials present and cleanup still pending. The diagnostic Go overlay lives only in root Temp and leaves the frozen candidate clean. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-stale-management-pg.jsonl`; session 39045 is collected. An initial diagnostic compile failure was corrected before these executions and is not counted as behavioral evidence.

Both findings were delivered as one concrete backend repair batch to the same main Luna/xhigh chat. Main may change the datasource repository and add a NEW owned concurrency regression test; the reserved auxiliary test, frontend and pure contribution files remain frozen. Requested regression coverage includes stale PUT/credentials/resume racing both clear and unbind, persisted stripped credentials/irreversible lifecycle and sanitized public failures. Root retains the PostgreSQL slot and handles host verification; no worker credential/cache or GitHub loop. No integration, issue closure or next-ticket assignment is authorized until these findings are repaired and independently reverified.

## Coordination

Main cursor processed through `24d59333-8ff6-4916-8a8c-2d74a9a4d6bb:54`, now active on P1 only. To shorten repair, root reused the original UI chat/tree (2119) on a new preserved branch `codex/t19-public-error-repair` at clean 335f5769 for P2 only. Its ownership is `internal/handler/datasource.go` plus NEW `datasource_source_lifecycle_error_test.go`; main must not edit these handler files. UI cursor processed through `42309ca2-9f92-400a-bbd8-1bb7c5c274f0:2`, active. Main owns lifecycle repository/service and NEW concurrency tests. The original contribution/auxiliary helper remains frozen/idle. Root owns the dedicated 57822 PostgreSQL slot. No ACK loops or premature T21 work. Acceptance remains **19/22**.

## Final repairs, integration and acceptance (2026-10-04)

P1 repair clean `e562395318be619eccece08f2f9cb46c41cef82e` changes only datasource repository plus NEW public-service concurrency regression. Sol/high narrow Standards 0 hard/0 material new judgements; Spec 0 hard. Root's actual six clear/unbind × PUT/credentials/resume cases ALL PASS, package44.948s/case39.81s, respectively10.49/5.70/5.86/7.10/5.28/5.38s. They reach generation/fence, stripped credentials, paused status, old file/chunk/read behavior, allowed fresh metadata and rejected mode escape assertions. Session38443collected; log `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-stale-repair-six-pg.jsonl`. Ordinary-document repository sync_deletions=false persistence regression actually PASS3.483s; session92489collected.

P2 public-response repair `d0af4c17` passed host response+existingcontracts4.059s, but narrow Spec found new raw-error logging against parent Spec39. The safe-type/code logging repair and request-scoped log capture froze as clean `eef8d54a58184e343a54d64a785bbe956fa569cb`; final narrow Standards0hard/0newjudgements, Spec0. Host HTTP+logs+existingcontracts actually PASS3.920s. One intermediate log test FAIL4.357s because existing LOG_FORMAT=json is interpreted as a literal template; the fixture now supplies an isolated request-context logrus entry, without changing global/application logging. All relevant sessions86154/22880/48297/76365collected. The exact-string domain classification remains a non-blocking Primitive Obsession judgement.

Root integrated P1/full main candidate and cherry-picked the two accepted handler commits as906905cf/cd87179e. Combined product head `cd87179ef39e582c07f3d3183bc48315e926ca07` independently passes all four normal PG gates again, package33.409s: cleanup failure/retry/history/physical reclaim/B isolation8.26s, unbind6.89s, clear scope isolation8.72s, stable repeatedintent4.99s. Combined HTTP/lifecycle/log regression PASS4.055s and DTO PASS1.110s. Sessions52243/45391collected; combined PG log `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-cd87179e-combined-pg.jsonl`. No root test process remains. No full repository test pass is claimed; earlier recorded environment limitations remain separate from these actual targeted passes.

T19/#27 is accepted: prior P1/P2 findings are repaired and independently verified. Overall **20/22**; T21/#29 may start from the final accepted integration commit, T22/#30 still waits. Runtime release remains subject to T22. Root will publish one acceptance milestone and closure, without process-comment spam. Root retains dedicated57822; workers must not inspect shared credentials or restart shared dependencies.
