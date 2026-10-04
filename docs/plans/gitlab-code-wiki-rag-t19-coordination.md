# T19: source lifecycle coordination and contracts

T16/#24 was accepted and published as `e2677b62e8d847d2585f103d634f4945c0839d50` after independent Sol/high review and seven integrated PostgreSQL gates (80.404s). Issue #24 is CLOSED; #27 is assigned to rudyvv. This is the user-approved automatic fixed point for T19, not a new request for review-scope approval. Acceptance remains 19/22 until the complete T19 path is reviewed and verified.

The original three execution chats remain Luna/xhigh. Root owns planning, contract changes, Sol/high dual review, real independent verification and integration. Proposal completion, compilation or a passing auxiliary slice does not close T19 or unlock T21.

| Role | Existing chat | Existing worktree | New branch | Ownership |
| --- | --- | --- | --- | --- |
| Lifecycle implementation | 01a0ebfe-8153-70e0-898e-2b7ffa91eaad | C:/Users/28211/.codex/worktrees/a8ea/WeKnora | codex/t19-source-lifecycle | Backend lifecycle, durable cleanup, schema, public management adapters and integration tests; no helper-owned files |
| Pure contribution withdrawal | 01a0ebfe-56df-7223-8644-ca717bd658bd | C:/Users/28211/.codex/worktrees/t16-impact-loader-errors/WeKnora | codex/t19-contribution-withdrawal | NEW internal/source/source_wiki_withdrawal.go, its focused test, NEW internal/types/source_wiki_withdrawal.go only |
| Management UI | 01a0ebfe-da20-71d2-a963-fa965754b497 | C:/Users/28211/.codex/worktrees/2119/WeKnora | codex/t19-source-management-ui | frontend datasource API, DataSourceSettings.vue/focused test and necessary existing locale entries only |

All old branches are preserved; no duplicate chats or worktrees were created.

## Lifecycle invariant

Pause and unbind stop new scheduling and invalidate old sync/Wiki writes, but preserve existing source/current-card/history query rights. Existing source soft deletion hides data through read predicates and therefore cannot implement the new unbind action. Ordinary document datasource deletion keeps its current behavior.

Explicit clear synchronously revokes source query rights, advances fencing generations and persists the cleanup intent in one transaction before returning success. Pending, running, failed and retried cleanup never restore those query rights. Cleanup remains bounded, durable and restartable. Pins do not block administrative clear; valid unrelated owners must survive. Current multi-source pages keep only independently proven survivor body and metadata. A historical revision containing the cleared source is removed in full, never rewritten to erase references while retaining the old body. The backend must discover complete current/revision ownership, including non-primary sources and exact evidence identities.

## Public management contract approved by root

Under `/api/v1/datasource/:id`:

- `POST /unbind` with `{}`.
- `POST /clear-source` with `{confirm: true, scope: "current_and_history"}`.
- `POST /clear-source/retry` with `{operation_id: string}` for the exact existing retryable failed intent.

Responses use the existing `{data: DataSource}` envelope. List/get also expose an optional source-only field:

```typescript
source_lifecycle?: {
  binding_state: 'bound' | 'unbound'
  query_enabled: boolean
  cleanup?: {
    id: string
    status: 'pending' | 'running' | 'failed' | 'completed'
    retryable: boolean
    error_code?: string
  }
}
```

`query_enabled` describes the source's withdrawal state, not a grant overriding caller permissions. Pause uses the existing paused status. Unbind reports unbound/query-enabled; clear acceptance reports unbound/query-disabled plus the durable intent. Repeated clear is idempotent for that same intent. No original credentials, SQL errors or internal paths enter UI error fields. Existing active/paused/error status and ordinary document interfaces are preserved. Unbound/cleared source actions cannot reactivate scheduling through resume, edit, validate or webhook configuration.

UI source detection uses `config.settings.content_mode === 'source'`, not connector type alone. Source unbind and explicit clear are distinct admin actions; ordinary document DELETE remains unchanged. UI follows authoritative replies and shows pending/failed cleanup, with exact-ID retry where permitted; it must never restore visibility locally after failure.

## Pure withdrawal interface approved by root

`WithdrawSourceWikiContributions(SourceWikiWithdrawalInput) (SourceWikiWithdrawalResult, error)` accepts an already-authorized complete owner projection with tenant/KB/page/source identity, an explicit required current/revision discriminator, primary page projection and typed contribution entries with title/summary/refs/provenance. Callers verify DB owner completeness and mark unknown affected body/owners unattributed.

The pure module returns unchanged, reproject or withdraw_page. Only current pages may be reprojected; affected revisions are withdrawn in full. All survivor output fields derive from independently validated survivors, preserving source-local repeated evidence IDs across sources. Scope errors are typed operational errors. Malformed, unattributed, mismatched or deterministically oversized proof inputs safely withdraw the projection without partial output. Existing contribution validators/clones are reused; composition matching has a bounded work/byte budget. This module does not grant permissions, use a model or touch persistence/pins.

Tests cover A+B current withdrawal versus complete historical withdrawal, unrelated B-only history, source-wide multiple topics, missing/invalid scope, unknown attribution, metadata/ref/body mismatches, immutability, stable ordering, same local evidence ID across sources and bounded overlapping-body matching.

## Environment and communication

Dedicated root-created PostgreSQL remains localhost:57822/source_test. The backend worker currently owns the slot; helpers do not run PostgreSQL. Only process dot-source `C:/Users/28211/.codex/test-runners/weknora-source-t16-57822-env.ps1`; never read or print its content, DSN, old container credentials or secret metadata. Shared application services are untouched. Root fetched the actual approved #27 text into `C:/Users/28211/AppData/Local/Temp/weknora-t19-approved-issue.md` for worker identity isolation; workers do not need to retry GitHub authentication.

Internal questions go directly to root thread 01a0e5ba-ea2d-7ea1-85e5-cb87f159495d as NEEDS_ROOT. If messaging is unavailable, the final status contains the concrete question so compact wait can collect it. One cache/git permission failure yields a dirty fix report for host handling, not repeated alternate-cache compiles. Root reviews a clean frozen SHA once; no automatic ACK loops or GitHub process comments. No helper edits another role's files. T21/T22 stay blocked until dependencies are accepted.


## Approved backend implementation and first reviewed slice

Root collected the revised backend proposal and approved implementation using the fixed public contract above. Its acceptance transaction follows existing sync-state -> datasource -> publication -> cleanup-operation lock order, with generation fencing, immediate query revocation, stopped triggers/credentials/webhook and durable intent committed together. The cursor/lease worker handles current projections, complete affected revisions, source-specific pins/owners, artifacts/index/raw/cache cleanup and finalization. Existing query leases must deny the cleared source while still permitting independent remaining sources; no new global lease check may unnecessarily revoke their scopes. Unbind does not introduce an automatic rebind flow. Source-mode legacy DELETE may preserve compatibility through unbind; ordinary document soft deletion remains unchanged.

The UI worker's declared writable tree was its original `2119` tree rather than the later `flow-config-diagram` tree. Root verified both were clean, preserved their old branches and moved the same T19 branch to the original 2119 checkout without reset, new worktree or permission bypass. All UI edits/tests now target 2119; flow-config-diagram retains the frozen regression branch.

Pure withdrawal freeze `7f630db417dfe828c3d48338f46ff744d822d4c2` is clean and changes only its three assigned new files. `git diff e2677b62...7f630db4` and its one-commit log were pinned for independent Sol/high dual review. Standards: **0 hard violations, 1 possible Duplicated Code judgement** (composition/reference matching resembles the existing service helpers; non-blocking). Spec: **0 findings** for the approved pure seam. The worker's complete source package tests PASS 4.427s. Root's actual nine `TestWithdrawSourceWiki*` focused checks PASS **3.706s**. An earlier root pattern matched no tests and yielded compile-only 3.820s; it is not counted as behavioral evidence. Sessions 14562 and 3302 are collected.

Root approved the main worker to cherry-pick this single frozen commit. This slice is not acceptance of T19's transaction, durable physical cleanup, public management or read-revocation behavior. The main and UI implementations remain in progress, and #27 stays open. No additional GitHub process publication occurred.

## Native verification during implementation

Root applied the accepted pure commit in the main tree as `9c18ef2b53095a2e89eb3f2884222b73e7036a65` after the worker reported its Git index permission failure. The backend implementation remains dirty and is not frozen or formally accepted. Root's DTO check first failed to compile because two composite literals called a pointer receiver; after the worker introduced named variables, `TestDataSourceResponse_SourceLifecycleIsSourceOnly` actually passed **3.413s**. Session 39115 is collected. Root also reminded the worker to cover repeated clear acceptance against an active cleanup: returning the same durable operation must not advance a generation while leaving that operation's fence stale.

Root installed frontend dependencies only in the UI tree from its existing lockfile with `npm ci --ignore-scripts --no-audit --no-fund`; session 69190 completed successfully and manifests/lockfiles are unchanged. The worker's Vue type-check passed, but its tsx invocation was blocked before collection by `os.userInfo()`/`uv_os_get_passwd`. Root then ran the normal locked tsx command on the host. The six real management tests finished in **3288.7866ms: 3 passed, 3 failed**. Endpoint/payload, pause and exact-operation retry passed. Clear, unbind and ordinary-document delete each failed their visible-menu assertion at lines 219, 235 and 294. Root sent these actual failures to the same UI worker for minimal render/selector or product diagnosis without relaxing the visibility/confirmation assertions. The host log is `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-ui-native.txt`; no PostgreSQL or shared application change was made for these checks.

The UI may resume its owned-file repair; it is not waiting for an uncollected host test. Native startup failures, syntax checks and incomplete backend SQL-string assertions are not substitutes for the eventual real management/read-revocation/cleanup tests. Main keeps the dedicated PostgreSQL slot until it explicitly hands the slot to root. The pure helper remains frozen and consumed, without an ACK loop or another ticket assignment. Acceptance remains **19/22** and T21/T22 remain blocked.

Root subsequently verified the selector correction: all six management cases PASS 4170.3968ms, and worker vue-tsc PASS 29.907s. The worker's one Git index denial was handled by host saving exactly eight owned files as clean `097435ffc8ad1cc81707ec795f051fa770fb9733`. Independent Sol/high review found 0 hard Standards breaches/1 non-blocking smell, and 1 P2 Spec race. Root independently reproduced the delayed pre-clear list overwriting a newer clear reply with the real Vue component. Candidate is not accepted; the same UI worker is repairing it. See `gitlab-code-wiki-rag-t19-ui-097435ff-review.md` for actual positive and negative evidence.

## Additional non-overlapping test slice

To reduce serial waiting, root reused the accepted pure helper for one test-only continuation of T19, not a new ticket or planning delegation. Its existing tree/branch stays unchanged; its new ownership is **only NEW `internal/application/service/source_lifecycle_review_integration_test.go`**. The main worker must not edit that filename. The accepted three pure files stay frozen.

Root fixed three gates for the helper to implement using actual existing PostgreSQL fixtures and approved lifecycle signatures: unbind keeps published raw/search/handle reads while fencing future sync; clear rejects old A handles while independent B remains readable through the same A+B lease; repeated clear returns the same operation and does not advance its active generation/fence again. Main continues physical cleanup, historical withdrawal and failure/retry coverage independently. The helper does not run PostgreSQL, invent public types, edit fixtures/product files or claim compilation/behavioral acceptance. Root will review its frozen file and execute it against the complete main candidate with migration 117 loaded after main hands over the test slot. Current backend signatures/types may be inspected read-only in a8ea; no dirty product merge is authorized. All three existing execution chats now have concrete, separate T19 work and direct root question/report authorization.

## Final frontend slice and auxiliary test handoff

UI freeze `ebec7cc223c9bf8b2c8576e44ad403c83b9d07cd` is accepted as a slice after Sol/high Standards 0 hard/1 new non-blocking duplication judgement and Spec 0. Root's eight management tests PASS 2819.9504ms, both independent ordering counters PASS 2241.4019ms; worker vue-tsc PASS 30.489s. The stale GET and delayed old unbind POST findings are both resolved. Exact evidence and earlier red results remain in `gitlab-code-wiki-rag-t19-ui-097435ff-review.md`.

Helper freeze `566cc595a550aaeecec33932e1acdea80264f286` changes only the reserved NEW 360-line service integration test. Static Sol/high Standards 0 hard/1 non-blocking duplication judgement; Spec 0 within the three read/fence gates. Standalone compile could not resolve five main-owned lifecycle constants; no actual PostgreSQL run or compilation pass is claimed for that standalone tree. The helper uses original migration 117 unchanged in a private fixture schema, and must not modify shared schemas or run against the main worker's exclusive slot.

Root consumed UI commits as `1e69b497`/`db1fca60`/`bc6987cb`, then the auxiliary test as `c42fea97a9a255106dd5d95d25c46b5c875b8cdc` in a8ea. Its frontend comparison to ebec is empty; dirty backend work was preserved. Main now owns further backend implementation and physical/history/recovery tests, while frontend, the three pure files and the auxiliary test remain frozen. The helper's direct completion message reached root successfully. The other two chats stay idle and frozen without ACK loops or premature next-ticket assignments.

Root resumed the same main Luna/xhigh chat after an actual quota failure, with concrete handoff and no duplicate chat or alternate-cache retries. Root is verifying the combined candidate's compilation and precise handler contract tests; these do not substitute for final lifecycle PostgreSQL gates. The renamed NEW `sourceCleanupEvidenceOwnerKey` avoids a collision with the existing Wiki evidence-owner type. Main retains exclusive localhost:57822 until explicit handoff. Whole T19 remains unaccepted, **19/22** overall.

The combined service integration package actually compiles: `go test -tags=integration ./internal/application/service -run '^$' -count=1` PASS **5.970s**, with no tests run. The precise HTTP lifecycle contract test actually PASS **4.062s**. Root collected sessions 26468 and 76812; no root test process remains. Main was given these results so it need not repeat compilation. Static pre-freeze checks identified the raw/disabled fingerprint combination advancing generations on repeated clear and a premature completion condition based only on retirement/GC enqueue. Both concrete implementation concerns were delivered to main before freeze, along with credential/webhook withdrawal requirements. Existing Git reads use temporary checkout plus deferred removal; no new arbitrary persistent-directory deletion is authorized. These are draft implementation checks, not a frozen whole-ticket review or a PostgreSQL acceptance claim.

Main explicitly handed the unused 57822 slot to root; its direct message failed but root collected the exact handoff from the recorded call. Root then actually ran all three auxiliary lifecycle PostgreSQL gates, package **FAIL 31.938s** (unbind 11.19s, isolated clear 10.34s, repeated clear 5.65s). All three stop at a shared product SQL error: `column "id" does not exist`, SQLSTATE 42703. `lockCurrentSourcePublication` orders by `id`, while production migration 102 defines `source_publications.data_source_id` as its primary key and has no `id`. This is a product/schema mismatch, not a missing runner or a failure of the fixture's lifecycle assertions. Those later assertions were not reached. Root sent the exact cause to main for a minimal lock-query repair, without modifying the fixture to invent a column. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-read-fence-pg.jsonl`; session 50841 exited 1 and was collected. The slot was explicitly returned to main; root has no remaining test process. No gate is claimed passing.
