# T19: source lifecycle coordination and contracts

T16/#24 was accepted and published as `e2677b62e8d847d2585f103d634f4945c0839d50` after independent Sol/high review and seven integrated PostgreSQL gates (80.404s). Issue #24 is CLOSED; #27 is assigned to rudyvv. This is the user-approved automatic fixed point for T19, not a new request for review-scope approval. Acceptance remains 19/22 until the complete T19 path is reviewed and verified.

The original three execution chats remain Luna/xhigh. Root owns planning, contract changes, Sol/high dual review, real independent verification and integration. Proposal completion, compilation or a passing auxiliary slice does not close T19 or unlock T21.

| Role | Existing chat | Existing worktree | New branch | Ownership |
| --- | --- | --- | --- | --- |
| Lifecycle implementation | 01a0ebfe-8153-70e0-898e-2b7ffa91eaad | C:/Users/28211/.codex/worktrees/a8ea/WeKnora | codex/t19-source-lifecycle | Backend lifecycle, durable cleanup, schema, public management adapters and integration tests; no helper-owned files |
| Pure contribution withdrawal | 01a0ebfe-56df-7223-8644-ca717bd658bd | C:/Users/28211/.codex/worktrees/t16-impact-loader-errors/WeKnora | codex/t19-contribution-withdrawal | NEW internal/source/source_wiki_withdrawal.go, its focused test, NEW internal/types/source_wiki_withdrawal.go only |
| Management UI | 01a0ebfe-da20-71d2-a963-fa965754b497 | C:/Users/28211/.codex/worktrees/flow-config-diagram/WeKnora | codex/t19-source-management-ui | frontend datasource API, DataSourceSettings.vue/focused test and necessary existing locale entries only |

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
