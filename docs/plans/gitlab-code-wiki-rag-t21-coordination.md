# T21 resource controls, deployment and observability

Approved ticket: rudyvv/myWeknora#29. Parent requirements: gitlab-code-wiki-rag-spec.md, especially 37–39. T19 accepted; 20/22 complete. T22/#30 remains the final acceptance gate.

Implementation and review base: `c0747374a96908b98c29aaa49526d7dbd0ec48f9`. The human approved using the accepted integration commit at ticket start. Root owns contracts, Sol/high dual review, independent tests and integration. Existing three chats implement with Luna/xhigh; no new chats or worktrees. Previous branches are preserved.

## Ownership

- Original T09, a8ea, `codex/t21-source-resources`: Go backend resource policy, source preview/sync/staging, scoped capacity accounting and actual run telemetry. Own internal/application, internal/types, internal/config and internal/source Go changes; related repository/container/model changes only where this ticket requires them. No Python/Docker or frontend edits.
- Original T06, actual t16-impact-loader-errors tree, `codex/t21-parser-operations`: sourceparser runtime, lock-preserving Docker deployment, request/process limits, offline and fault tests, deployment/preflight instructions. Own sourceparser/, docker-compose.source.yml, dedicated new scripts/source-* operations scripts and docs/deployment/source-operations.md. No Go product or frontend edits. A Go seam request goes to root for assignment to T09.
- Original T10, 2119, `codex/t21-source-observability-ui`: frontend typed API and source run observability components/tests/locales. Own frontend only. No guessed backend state or new endpoint. Preserve T19 response ordering and source lifecycle controls.

After UI slice acceptance (9d3be46f, actual nine component/log tests and thirteen locale tests green, independent dual review complete), the same T10 chat/tree continues this ticket on `codex/t21-source-resource-regression` from the original c074 base. It owns only NEW `internal/application/service/source_resource_review_integration_test.go`, integration-tagged independent resource-rejection/cancellation tests at the approved public seams. Main must not touch this reserved file. Helper reads main's real pending policy/controller and fixtures; no product/schema changes or standalone/PG pass claims before combined verification. Root owns the PG run. This is T21 assistance, not T22 or a new ticket.

Each slice reports a clean frozen SHA, exact changed files, actual tests and limitations. Complete one slice before further work; root must review and consume it before assigning the next ticket. Infrastructure/contract questions go directly to root thread `01a0e5ba-ea2d-7ea1-85e5-cb87f159495d` with NEEDS_ROOT. If messaging is blocked, leave a concise NEEDS_ROOT final record for compact polling; do not ask the human to find internal runners. No ACK loops or issue process comments. Root publishes only accepted milestones.

## Resource contract

Reuse the existing background per-model governor across document/source/Wiki/Embedding clients. Ensure source and Wiki work is marked background and cancellation does not start a provider call after waiting. Do not invent a second provider quota or change interactive request behavior. Source work has a separate finite admission limit; document and Wiki worker lanes keep independent bounds. Describe honestly whether each bound is process-local or distributed. Configuration errors must fail clearly, not disable bounds silently.

Replace the prototype 100-file/16-MiB whole-run restriction consistently in preview and execution. Deployment defaults: maximum 10,000 selected files, 512 MiB selected original bytes per run, 16 MiB per file, one concurrently active source run per app process, 30-minute run deadline. Values are finite and configurable through a cohesive source resource policy; existing per-file rules may impose a lower limit. Do not retain all selected raw files in one map: spool verified immutable blobs to a private bounded temporary stage, or use an equally bounded streaming seam. Keep parsed/index/relation allocations bounded as well; reject resource exhaustion before publishing. A full manifest cannot be labeled complete if truncated. Large-repository performance is measured in T22, not promised by these defaults.

Count cache, temporary/staging, retained original and vectors separately. Distinguish logical payload bytes from physical database/free-space measurements and label their scope. Capacity checks must reserve headroom and return stable source errors on exhausted budget or disk; preserve current/history/pinned evidence. Reuse existing retention/GC ownership; no eviction of protected raw versions, unsafe recursive deletes or invented persistent clone storage. Git transport staging itself needs a finite disk/transfer/time bound, not just a post-fetch selected-file count. Do not expose paths, credentials or raw SQL in public errors/logs.

Root resolved the backend's capacity question: cumulative Git transport defaults to 1 GiB, private Git object-stage disk to 1 GiB and selected-blob spool to 512 MiB, independently enforced and counted. Temporary volume free-space reserve is the larger of 256 MiB and 20% of volume capacity. Source-scoped logical payload quotas default to 2 GiB retained originals, 512 MiB parsed artifact cache and 4 GiB vector float32 payloads; include uncollected history, pins and stages with identity deduplication. Enforce scoped aggregation/reservation inside the existing writer/fence boundary. These are not physical database-space measurements. Where database volume free space cannot be reliably measured, leave it unknown, document a deployment volume check and preserve the publication on actual database disk-full failure. Keyword/index metadata overhead is not silently included in float32 payload accounting. Reclaim only through existing ownership-proven safe GC, otherwise fail clearly. Backend supplies actual configuration names to root/deployment owner before freeze.

Parser defaults retain Docker 768 MiB/2 CPU/64 PIDs and tmpfs 32 MiB, locked Python/official Node/grammar build assets, nonroot/read-only/internal network. Add finite accepted HTTP connections/body read time/request bytes/child work/response bytes and cancellation/timeout teardown. Two parser child slots remain default. Do not bump locked dependencies casually. Lite connects the same external parser endpoint. Internal CA and connectivity preflight must report checks without dumping secrets or fetching dependencies at runtime.

## Additive public telemetry contract

Use the existing `SyncResultDetail.source.snapshot` and `source.members`; source phase remains `SyncLog.source_run_phase`. Add optional `source.telemetry` to `types.SourceRunResult` and the matching frontend type, persisted with the run result. Schema version 1:

```ts
interface SourceRunTelemetry {
  schema_version: 1
  phase_duration_ms?: Record<string, number>
  selected_bytes?: number
  storage?: Partial<Record<'cache' | 'staging' | 'original' | 'vectors', {
    used_bytes: number; limit_bytes?: number; measurement: 'logical_payload' | 'physical';
  }>>
  quality_counts?: Record<string, number>
  model_usage?: { embedding_calls?: number; generation_calls?: number;
    input_tokens?: number; output_tokens?: number; estimated_input_tokens?: number }
  lease_recoveries?: number
  cleanup_residue_count?: number
  published_commit_sha?: string
  wiki_coverage?: { eligible: number; ready: number; stale: number; failed: number;
    ungenerated: number; deferred: number }
}
```

Only bounded allowlisted phase/quality keys; numbers nonnegative and finite. Phase-duration keys are fetching, parsing, indexing and publishing: cumulative actual execution milliseconds for this sync run, retaining persisted totals on recovery; queued/wait time and asynchronous Wiki generation are separate. Quality keys are structural, partial, syntax_error, degraded, unknown_preprocess and text_fallback: one overall parsed-file quality count per snapshot file, including reused files, not per chunk or region. UI localizes known keys and ignores invalid keys/numbers. Missing means unmeasured/unknown, zero means actually observed zero. Actual provider token usage is separate from estimates; retries count actual calls and phase duration scope is explicit. Storage classes cannot double-count without an explicit definition. Published SHA comes from the authoritative publication, never the target of a failed run. Wiki coverage comes from the current scoped inventory/status with complete semantics, never run progress or guessed member count. Runtime/cross-run counters must not reset into misleading zeros on recovery. If an existing API already supplies a measurement, reuse it instead of adding another state source. Any unavoidable schema adjustment is a concise NEEDS_ROOT proposal before mutation; root coordinates both backend/UI.

Root's 2026-10-05 pre-freeze interface check caught unpublished backend storage key/measurement drift; main was told to retain the four accepted UI keys and enum values. `storage.staging` measures logical temporary-file payload: private Git object file lengths plus verified blob spool content lengths, with the sum of their configured limits. Each internal limit remains separately enforced. It does not measure allocated filesystem blocks or volume free space, and is not a total across staging and retained originals. `storage.cache` measures source-scoped parsed-artifact JSON payload. An optional additive `git_transfer_bytes` may record actual cumulative transport separately; no new frontend row is required. These are concrete measurement definitions, not permission to leave measurable call/coverage fields unimplemented or claim zero for unknown data. Scoped vector-row aggregation must deduplicate actual row identity.

UI displays detected/target/published SHA, phase, real counts/reuse, parse quality, Wiki coverage, last successful publication and measured telemetry with unknown states for old/missing fields. Source-only views leave ordinary document logs intact. Existing SourceSnapshotRunView is the primary seam, not a duplicate source dashboard.

## Verification and environments

Pre-approved TDD seams: public datasource preview/sync and sync-result DTO, existing source repository publish/GC transactions, real parser HTTP protocol, source operations Vue components. Test externally visible failures, retained old reads, cross-source isolation and measured limits; no tests mirroring implementation internals.

Root owns the dedicated PostgreSQL slot on localhost:57822. Safe runner path: `C:/Users/28211/.codex/test-runners/weknora-source-t16-57822-env.ps1`; only root process dot-sources it. Do not read/print/send its contents or DSN, inspect old container credentials, restart shared services, or ask the human for an internal path. Workers hand stable code/test candidates to root when host-only verification is required; no cache/permission workaround loops. GitHub issue copy is at the root-created Temp file `weknora-t21-approved-issue.md`; a worker 401 is not a reason to retry login or stop implementation.

Docker fault/offline verification uses uniquely named isolated containers/networks/volumes, never the user's running app or shared dependencies. Record real image/CPU/memory/storage settings and assertions: offline startup and parsing, parser child crash and timeout, concurrent request saturation, memory pressure, disk-full failure, restart recovery, ordinary document/model-governor regression. Docker parser fault tests alone are not whole source publication/PG acceptance. Root joins backend and parser slices for the source end-to-end gates. No performance claims without measurements; no secret or representative full source repository committed.
