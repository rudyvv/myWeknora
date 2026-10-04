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

Main cursor processed through `24d59333-8ff6-4916-8a8c-2d74a9a4d6bb:53`; it is idle on the freeze hold. Root owns the dedicated 57822 PostgreSQL slot. The two consumed helpers remain frozen/idle, without ACK loops or premature T21 work. Acceptance remains **19/22**.
