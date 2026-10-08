# Source acceptance runner

GitLab Push Webhook：**已有实现，端到端验证未完成，暂不对外提供**。
源码模式使用“立即同步”和定时同步，源码发布后继续通过持久化通知更新 Wiki。
前端 Webhook 入口和配置弹窗已移除；后端不注册回调或管理接口，服务层拒绝调用。
迁移 `000122` 停用已有 Webhook 配置，并取消未完成的 Webhook 同步任务；已有源码和 Wiki 发布保持可用。
保留的 Webhook 实现与模拟测试仅供后续开发验证，不能作为真实 GitLab 端到端验收证据。

源码与文档模式统一使用“删除”数据源：结束接入并停止同步，保留已同步知识。
源码删除同时撤掉连接凭据、停止待执行和运行中的同步及 Wiki 派生工作；保留历史身份以支持已有源码检索、Wiki 与引用阅读。
不再提供独立解绑、清除源码知识或清理重试的前端操作及公开 API。旧清理记录与后台处理仅用于兼容此前已接受的操作，不属于当前接入流程。

`scripts/source-acceptance-run.ps1` performs a bounded acceptance pass against the existing WeKnora API. It verifies model and source scope, obtains source previews, associates each source with its approved published snapshot, issues 30 source-scoped top-10 searches, then checks expected evidence through authorized original-file reads. Its default mode does not publish or otherwise mutate sources.

The WeKnora bearer token must be injected into the process environment as `WEKNORA_ACCESS_TOKEN` by the approved secret mechanism. The runner never reads GitLab credentials (they remain in the configured datasource), accepts no credentials as command arguments, suppresses API error bodies, and never places source text or answers in the report. Avoid PowerShell transcription during a run that handles credentials.

## Inputs and fixed evidence scope

Required inputs identify the API origin, one knowledge base, a version-1 question bank, a report path, and the exact embedding model, tokenizer, and input-token limit. The runner verifies every referenced datasource belongs to that KB, is GitLab source mode, and is bound for reads. It checks the KB's selected embedding model and reads `/api/v1/models/{id}` to verify the model type, tokenizer, and configured hard input limit. The provider-documented hard limit must be at least the configured input limit. The provider-limit reference and hardware description are recorded metadata, not independently verified claims.

The checked-in T06 question-bank shape is accepted directly: exactly 30 entries, each with `id`, `category` (`symbol_path`, `business_chain`, or `frontend_sql`), `question`, `repository`, and one or more `evidence` records. Each evidence record contains a relative `path`, one-based `start_line`/`end_line`, and whole-file SHA-256; a record can override `repository` for cross-repository chains. The bank's top-level `hash_mode` governs the evidence digests. `sha256_utf8_lf` means SHA-256 over UTF-8 file bytes after CRLF and lone CR are normalized to LF. The authorized source API reports a separate raw-byte SHA-256; the runner verifies that identity first, then applies the declared bank hash mode. It never conflates those hashes.

The question bank must have status `human_confirmed` or `approved_for_acceptance`, or `-ApprovalMarkerFile` must provide schema v1 with `status: approved`, the exact question-manifest SHA-256, an approver, and a timestamp. A `draft_for_human_confirmation` bank is rejected by default. `-AllowDraftQuestions` is exploratory only: even if searches run, the report is `draft_not_scored`, the threshold remains false, and exit code 3 prevents treating it as release acceptance. Repository-backed question banks must declare an explicit supported top-level `hash_mode`.

`-RepositorySourceMap` binds each question-bank repository ID to an already published source in the selected KB. Every referenced repository must be mapped; an unmapped repository fails closed and is never silently assigned to `-DataSourceId`. Mapping file shape:

```json
{
  "schema_version": 1,
  "status": "approved",
  "knowledge_base_id": "<acceptance-kb-id>",
  "repositories": [
    {
      "repository_id": "evip_mobile",
      "source_id": "<mobile-source-id>",
      "snapshot_id": "<mobile-published-snapshot-id>",
      "commit_sha": "<mobile-commit-sha>"
    },
    {
      "repository_id": "nsb",
      "source_id": "<nsb-source-id>",
      "snapshot_id": "<nsb-published-snapshot-id>",
      "commit_sha": "<nsb-commit-sha>"
    }
  ]
}
```

The runner verifies each mapping's datasource, KB, previewed commit, and current published snapshot/commit before searching. Each question sends only the source IDs required by its expected evidence. All expected evidence spans in a question must be found in that question's top 10 and pass original-file verification for the question to count as `matched`. If one span in a cross-repository chain is absent or unauthorized, that question remains `unknown`. `-DataSourceId` is a compatibility option for a small single-source manifest whose evidence has no repository IDs; it cannot replace mappings for the T06 gold bank.

The runner reads datasource settings from the existing response and sends them only to `POST /api/v1/datasource/{id}/source-preview`, which does not persist the draft settings. Settings with credential-like field names are rejected. The preview's parser-readiness flag is the API-level signal that the app reached a ready, versioned parser over the private parser HTTP interface. An explicitly requested real sync exercises the app's `/v1/parse` calls; fake-server tests do not claim to run a parser process. Preview inventory in the report is limited to relative path, Git blob SHA, size, and status.

Every search calls `POST /api/v1/knowledge-bases/{id}/hybrid-search` with `match_count: 10` and only that question's selected `source_ids`. Expected bank paths and every returned hit—not just a gold-path candidate—must use a canonical Git relative path. Each hit must belong to the selected source's approved published snapshot and commit, have a valid line range, and pass `GET /api/v1/knowledge/{knowledge_id}/source?version_id=...` under normal KB authorization. The returned knowledge/source/snapshot/version/commit/path and raw-byte SHA must match the hit, and the hit's range must fit the authorized file's line count. `score` must be a finite JSON number or null; `match_type` must be the numeric backend enum (0–9). A denied, staging, malformed, or out-of-range hit fails the run closed. Each question's expected evidence is then compared to these verified top-10 records; a question counts only when every expected span matches and its whole-file digest matches the bank's declared `hash_mode`. Search answer wording is not scored. The 27/30 threshold is only this runner's top-10 evidence threshold, not full Recall or acceptance of Issue #30.

The runner performs an authorized fixed-version read for every hit (up to 300 reads per 30-question run); it does not reuse a prior read to bypass authorization. File contents are transient and are never added to the report or validation cache. The cache contains only fixed identity, raw SHA-256, and line count, with hard limits of 300 entries and 512 KiB. The report records the read count and cache usage.

Source preview and manual sync are Admin operations. Retrieval and original-source reads require the corresponding KB read authorization. A denied source read cannot count as a hit.

## Running

Run with PowerShell 7 or later. Production API calls require HTTPS and normal certificate validation. HTTP is accepted only for localhost/loopback fake-server tests.

```powershell
# WEKNORA_ACCESS_TOKEN is injected into this process by the approved secret provider.
./scripts/source-acceptance-run.ps1 `
  -BaseUrl 'https://weknora.example.internal' `
  -KnowledgeBaseId '<acceptance-kb-id>' `
  -RepositorySourceMap './artifacts/approved-source-map.json' `
  -QuestionsFile './docs/acceptance/source-representative-questions.json' `
  -ApprovalMarkerFile './artifacts/question-bank-approval.json' `
  -ReportPath './artifacts/source-acceptance-report.json' `
  -ModelIdentifier '<exact-kb-embedding-model-id>' `
  -Tokenizer 'cl100k_base' `
  -ConfiguredInputTokenLimit 8192 `
  -ProviderDocumentedHardLimit 8192 `
  -ProviderLimitReference 'provider:model-card#max-input-tokens' `
  -HashMode 'sha256_utf8_lf' `
  -HardwareDescription 'acceptance host CPU/RAM and app/database deployment IDs'
```

`-HashMode` is an assertion/compatibility input for banks without a top-level hash mode. For a repository-backed question bank, the bank's top-level mode is authoritative and the supplied option, if any, must agree. Omit `-ApprovalMarkerFile` only when the question bank itself has an accepted status.

By default the run is read-only and requires each existing publication to match the preview and approved map. `-Publish` is supported only for one legacy `-DataSourceId` run without `-RepositorySourceMap`; it calls the existing manual-sync endpoint, polls that newly returned log ID with a deadline, and verifies the published commit equals the immediately preceding preview and the commit pinned by each evidence item. A legacy evidence item may omit `snapshot_id`; only after that verified publication does the runner bind the missing value to the actual new snapshot. An explicit snapshot ID is never rewritten and must equal the just-published snapshot or the run stops. Mapped representative runs stay read-only; the approved 30-question mapping is never auto-replaced. Publish/review a candidate separately, then update the human-approved source map before scoring it. Never point `-Publish` at a production or shared source merely to obtain a report.

Each request is bounded by `-RequestTimeoutSeconds`; the full run has `-RunTimeoutMinutes`. A publish poll has `-SyncTimeoutSeconds` and `-PollIntervalSeconds`. Sync-log lookup paginates 100 entries per page for at most 100 pages and never infers a published SHA from a failed run.

An optional UI/Agent smoke can use the frontend's existing `POST /api/v1/agent-chat/{session_id}` endpoint. Supply `-AgentSessionId`, `-AgentId`, and `-AgentQuestion` together. It makes one model-backed request and persists a turn in that session, so use only a designated acceptance session. Every `response_type` must be in the backend's closed response enum; when an event also supplies `type`, it must be known and agree. The stream is `completed` only after a terminal `response_type: complete` event with `done: true`; empty, errored, malformed, truncated, or partial streams fail closed. The report stores only validated event types and a query hash, not raw event fields, the question, answer, or streamed source text. This checks the UI's chat API path, not browser rendering.

## Report and unknown measurements

The UTF-8 JSON report uses `schema_version: 1` and distinguishes runner execution from whole-ticket acceptance. Top-level `status` describes only this runner's retrieval execution. `t22_acceptance_status` remains `unknown`: root must evaluate the semantic Wiki/model, performance, and other Issue #30 gates; this runner never promotes a 27/30 result into whole-ticket completion. The report includes:

- Explicit KB, datasource IDs, published snapshots and commits; model ID, tokenizer, input limits, evidence hash mode, provider reference, and hardware description.
- Per-source preview/parser readiness, selected file and byte counts, and a reduced path/blob-SHA/size/status inventory; publish result and app-observed telemetry are separate.
- For each question: original ID/category, repository/source/snapshot/commit scope, every expected path/line/hash span, top-10 evidence identities/ranges and hashes, line counts, authorized-read outcome, and `matched` or `unknown`.
- Aggregate authorized-read count plus metadata-cache entries/bytes and their hard limits. The cache excludes source contents.
- Full-run and 1/10/100-file incremental plus same-budget text-baseline input/output slots. Without provided measurements these stay `unknown` with null values; they are never silently recorded as zero.
- Optional Agent/UI smoke status, event types, and query hash without answer text.

An optional `-MeasurementsFile` provides externally collected full-run, incremental, and text-baseline measurements. It is JSON with `schema_version: 1`, optional `full_run`, `incremental_runs` keyed by `changed_file_count` 1, 10, and 100, and optional `text_baseline`. Every record uses the same nullable metric contract: `selected_files`, `selected_bytes`, `chunk_count`, each `phase_duration_ms` value, `elapsed_ms`, `peak_memory_bytes`, `estimated_input_tokens`, `actual_input_tokens`, `embedding_calls`, and `generation_calls`. Incremental entries must declare matching `model_identifier`, `tokenizer`, and `context_limit_tokens`; the baseline uses the same model, tokenizer, and `budget_tokens`. A full-run record uses the declared model/tokenizer/input budget and, when marked measured or containing any metric, must pin `source_id`, `snapshot_id`, and `commit_sha` to a selected published source. Metric fields are strict nullable JSON integers: only JSON integer tokens in range 0..Int64.MaxValue are accepted. Numeric strings, booleans, decimal or exponent-form tokens (even if mathematically integral), non-finite values, negatives, and values beyond Int64 are rejected. The report preserves valid numbers as numbers and gives each metric an `observed` or `unknown` status; estimated tokens never stand in for actual tokens. A record is `measured`/`complete` only when all fields in that contract are observed, `partial` when some are observed, and `unknown` when none are. Missing full-run, 1/10/100 cases and same-budget baseline stay unknown, never zero. This file records external measurements; it does not prove the runner performed those scenarios. Even complete measurements do not change `t22_acceptance_status` from `unknown`. Make all 1/10/100 changes only in an isolated acceptance copy. The runner never modifies a repository to synthesize incremental changes.

This runner does not curate or human-confirm questions, generate/semantically review Wiki cards, synthesize repository changes, execute the fault/Agent regression matrix, or establish representative-repository performance thresholds. It is one repeatable API-evidence tool, not the complete Issue #30 gate. A successful invocation is not a claim that overall CodeWiki release acceptance passed.

## Independent fake-HTTP tests

The process-level tests invoke the real PowerShell runner against an isolated local fake API. They cover pagination, model and parser-readiness checks, report privacy, all-hit authorized reads, non-gold staging snapshot/version and out-of-range failures, canonical evidence paths, numeric score and match-type validation, bounded metadata caching, distinct cross-repository source identity, T06 question/evidence shape, multiple required evidence spans, CRLF-to-LF hash normalization, denied original reads, strict integer-token metrics including phase and match-type decimal/exponent rejection, partial/full-run measurement scope and Int64 preservation, fresh legacy publication binding without rewriting explicit snapshots, allowlisted terminal Agent SSE behavior and incomplete/error streams, and bounded sync timeout. They do not start Docker, PostgreSQL, GitLab, or a model service.

```powershell
go test ./scripts -count=1
```
