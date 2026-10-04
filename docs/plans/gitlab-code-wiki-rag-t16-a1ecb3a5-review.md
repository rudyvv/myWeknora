# T16 whole-ticket review — a1ecb3a5

Date: 2026-10-04. Candidate: `a1ecb3a5d1f6a988d9513cca7e25fad744df4f68`, clean in `C:/Users/28211/.codex/worktrees/a8ea/WeKnora`, branch `codex/t16-wiki-incremental`. Fixed point: user-approved `55d69df1355fba044ac0e58bc8f2f4dc2c15d2c0`. Scope: `git diff 55d69df1...a1ecb3a5`, all 37 changed files and five implementation commits. The accepted pure contribution/impact slices do not establish acceptance of their production adapters.

Spec: GitHub #24, independently read back OPEN; local `gitlab-code-wiki-rag-tickets/16-wiki-incremental.md`, approved aggregate-work and conservative-fallback coordination contracts. Standards: AGENTS.md, CONTEXT.md, docs/agents, applicable ADRs including ADR-0010, plus the code-review skill's judgement-only Fowler baseline. Two independent GPT-6 Sol/high reviewers assessed Standards and Spec. Root independently validated behavior using real PostgreSQL and parser HTTP fixtures.

## Standards

Two hard P2 findings; no additional actionable smell.

1. **Deterministic proof overflow is retried indefinitely.** `repository/source_wiki_impact_inventory.go` returns generic errors for capped member/fact/byte/relation inventories and invalid materialized proof. `service/source_wiki_update_processor.go:79-87` forwards them; the delivery is released for retry. ADR-0010 requires a durable source-wide-stale fallback when proof is incomplete or over budget. Distinguish deterministic bounded-proof errors from transient database failures; persist a fenced fallback with no partial unaffected/removal claims. Real root counter with 200,001 facts fails at the loader; it never settles the pending plan. Publication does already fence old answers stale: this finding concerns durable termination, not demonstrated unauthorized visibility.
2. **Adapter scans escape the total-work bound.** `sourceWikiParsedIDs(next)` at processor lines 270, 390 and 432 rescans the full parsed-member inventory for each topic; flow dependency fallback also scans members/facts per topic. The pure planner's meter cannot bound work already performed by its caller. Precompute shared indexes and charge aggregate adapter work/references before continuing, falling back atomically on exhaustion. This is a static confirmed finding, not a measured timing result.

## Spec

Three findings, worst P1. The overflow finding also appears on Standards; the axis counts are intentionally separate.

1. **P1 — affected regeneration and skeleton rescan have no consumer.** Ticket lines 7 and 11-13 require affected updates and checking newly introduced entry points. Processor lines 629-703 create pending `regenerate`/`skeleton` items, then mark the plan completed; the worker acknowledges/deletes its sole delivery. No existing consumer hands these items into T15/T17 generation. Root actual publication counter: plan completed, module/src regenerate pending, zero next-snapshot attempts/batches, provider calls unchanged at 2. Persisted planning alone does not implement regeneration. Connect bounded durable work to the existing generation/batch budget, recovery and publication fences; keep model calls outside database transactions.
2. **P2 — attributed mixed-source deletion is rejected.** Ticket line 13 requires only the corresponding source contribution to be withdrawn or updated. The processor's single-contribution guards at lines 769-773 and 899-905 reject a page containing independent, explicitly attributed A+B contributions. Root generated valid contributions for both published sources, assembled their attributed page, then completely removed source A's member: both contribution rows remained. Support exact-source removal/replacement and safe projection recomposition while preserving B's evidence/body and ordinary documents. Unattributed legacy prose must still remain conservative; do not guess paragraph ownership.
3. **P2 — deterministic oversized inventory does not reach the approved durable fallback.** Same actual loader/worker failure as Standards finding 1, violating the approved T16 over-budget contract. Do not treat a perpetually pending retry as a completed conservative result.

A tentative nested-module dependency finding was withdrawn. A small fixture was protected by all model-visible evidence; an 18-file fixture had 18 module members and 16 evidence dependencies but remained stale. Neither establishes an unsafe carry-forward. An initial fixture assertion expecting one dependency where two were correctly stored was a test setup failure, not a product defect. No repair was demanded solely from this unproven claim.

## Independent verification

Real PostgreSQL plus real local parser HTTP, with per-test isolated fixtures and a local test model provider:

- PASS: typed publication/outbox atomic acceptance, 9.11 s.
- PASS: unaffected module applicability across publication, 9.43 s.
- PASS: mixed ordinary document authorization and existing Wiki union behavior, 5.32 s.
- FAIL (product): affected publication creates no generation handoff, 8.25 s.
- FAIL (product): 200,001-fact proof errors rather than settling conservative fallback, 9.37 s.
- FAIL (product): attributed A+B page retains deleted A, 14.64 s, package 18.959 s.
- PASS (calibration only): 18-file uncited nested-member fixture remains stale, 31.57 s, package 35.702 s. This does not prove all nested-membership scenarios are correct.

Root used a temporary Go overlay; candidate production and test files remain untouched. Evidence: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-a1ec-pg.jsonl`, `weknora-root-t16-a1ec-nested-pg.jsonl`, `weknora-root-t16-a1ec-mixed-pg.jsonl` and `weknora-root-t16-a1ec-integration-overlay.json`. The original two-file nested assertion and the first overlay compilation error were fixture/setup failures and are excluded from product counts. All root sessions have been collected.

The old temporary 57821 runner was missing. Root created a separate authorized review database `weknora-source-t16-review-57822`, bound only to 127.0.0.1:57822, and verified readiness. Process-only environment runner: `C:/Users/28211/.codex/test-runners/weknora-source-t16-57822-env.ps1`. Credentials were generated for this new container, never read from prior Docker metadata, never printed or sent to a chat/GitHub. Missing parser dependencies were installed from the hash-locked wheel requirements into a separate review dependency directory, leaving shared environments unchanged. Existing whole-build SQLite-header limitations are not claimed resolved.

## Repair ownership and decision

The same three original Luna/xhigh chats were resumed with explicit non-overlapping responsibilities:

- Main a8ea: production generation/rescan consumer, durable recovery/fences, multi-source projection, adapter total-work bound, processor handling of typed fallback. Baseline 55d unchanged.
- Original T06/b0df: new branch from a1ec, only repository impact inventory/error classification and separate repository tests. No concurrent processor or migration edit.
- Original T10/contribution chat: new branch from a1ec, only a new service integration regression file. Root released exclusive 57822 access to this chat for one focused red run; it must release the database to main immediately afterwards.

Internal environment/interface questions go to root, not the user. New clean freezes need formal slice review and necessary independent validation before assembly; whole-ticket re-review follows the repaired candidate. No issue closure, root product integration or T19 dispatch. GitHub gets the next acceptance milestone rather than process spam.

Decision: **REJECT a1ecb3a5 for whole T16 acceptance; repairs dispatched.** Pure slices remain accepted. Total accepted tickets remain **18/22**. Standards: 2, worst P2. Spec: 3, worst P1.

## Loader interface decision

Root approved and relayed the loader assistant's concrete proposal: exported `repository.SourceWikiImpactLoadError{Failure, ReasonCode}`, with exported failure constants for `proof_incomplete`, `invalid`, and `budget_exceeded`, and `Unwrap` to `ErrSourceWikiImpactProofIncomplete`, `ErrSourceWikiImpactSnapshotInvalid`, or `ErrSourceWikiImpactLoadBudgetExceeded`. Main can use `errors.As` for the stable reason and `errors.Is` for classification. Errors return a zero snapshot and nil materialized result, never partial proof. Only deterministic materialized-proof failures within a fixed authorized snapshot qualify; scope/identity mismatches, unauthorized/missing snapshot lookups, SQL, connection and cancellation errors remain operational failures. Incomplete manifest/relation flags and actual-count mismatches must not be treated as complete proof. Main retains responsibility for fenced durable fallback and bounded generation handoff; the loader does not write plans or call providers. Proposal approval is permission to implement, not slice acceptance.

## Loader slice review — 0d449e62

Verified clean `0d449e6232c10cb387831629069b2cb3174d010a`, actual new tree `C:/Users/28211/.codex/worktrees/t16-impact-loader-errors/WeKnora` (the original b0df pure tree remains at accepted 9dc). Only the three authorized repository files changed relative to a1ec. Both independent Sol/high axes reviewed this slice against the approved T16 fixed point and concrete interface contract, with a1ec as the repair delta reference.

Standards: one hard P2; Spec: one P2, same boundary. Parsed identity validation at loader lines 151-158 and excluded-member validation at 189-194 classify joined files belonging to a different tenant/KB/source as typed deterministic invalid proof. The approved contract reserves these scope violations for operational errors. Separate real foreign-scope linkage from malformed local proof fields; main must not acknowledge an authorization/integrity failure as an ordinary completed stale fallback. No unauthorized content read was demonstrated by this counter.

Root independently ran the six existing top-level focused loader tests: PASS, package 3.485 s. These are SQL-mock/native tests, not PostgreSQL integration. A temporary overlay added parsed/excluded × foreign tenant/KB/source cases using an otherwise authorized snapshot: all six subcases actually FAIL, package 3.591 s, with typed parsed/excluded identity errors. Original files remain untouched. Evidence: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-loader-0d449e62-scope.jsonl`, `weknora-root-t16-loader-scope-fragment.txt`, `weknora-root-t16-loader-scope-overlay.json`. Both root sessions are collected. Budget preflights, stable reasons and zero/nil failures otherwise passed review. Narrow classification repair and negative regressions were sent to the same helper; main was told not to assemble 0d yet. No acceptance/whole-ticket closure.

## Regression slice review — 3e5eac88

The contribution helper froze `3e5eac887dbf44f612f31086a102edb9f99d8164`, only new `service/source_wiki_incremental_review_integration_test.go`, clean at the existing flow-config-diagram tree. Its one authorized PG run ended during parser setup (`cannot import name exp from sqlglot`) before all three behavioral assertions; this is not product red or green. Root's runner in the main tree independently imports sqlglot.exp successfully from the isolated review dependency directory; no shared installation or credentials were required. Helper released the PG slot; main now owns exclusive 57822 access.

Root found two fixture/assertion bugs before test acceptance: A and B generated the same prose, making removal's NotContains assertion contradict preserving B's exact body; generation waited asynchronously but inspected an item loaded before the wait. The same helper is repairing distinct attributed A/B outputs, reloading durable state and tying terminal generation to the affected module topic. No production edits or further helper PG run authorized during main's exclusive slot. Main was told not to assemble 3e5 yet; final repaired test freeze needs review and real behavioral validation. Cross-thread message rejections were honored; root collected the completed result through compact wait and relayed the necessary direction without human environment questions.

## Quota recovery and loader acceptance — 1056f062

After the user's quota recovery, compact snapshots established that main and loader turns had failed with usage-limit errors, while the regression helper had completed edits but stopped on index-lock permissions. Root preserved all dirty/staged work, resumed the same three Luna/xhigh chats, and independently checked the persistent runner: parser sqlglot.exp/tree-sitter imports PASS and dedicated PostgreSQL readiness PASS. Main retains exclusive 57822 access; no shared service/runtime change or credential inspection occurred.

New loader clean freeze `1056f062e86f3c23d74c5c20fb828b1a50e4166d` has a pre-switch foreign-file-scope check returning an operational error, covering parsed and excluded records, with zero snapshot/nil loaded. Local missing parser metadata remains deterministic typed invalid; valid excluded members without source-file identity remain allowed. Two independent Sol/high axes re-reviewed the whole loader slice and repair delta: **Standards 0 hard / 0 smells; Spec 0**.

Root reran the original six focused loader tests plus original six foreign-scope overlay counters: PASS, package 3.413 s. That overlay contains the original frozen test file, so root separately ran current-HEAD native tests: all eight top-level focused tests, including worker scope and missing-parser regressions, PASS, package 3.502 s. Logs: `weknora-root-t16-loader-1056f062.jsonl` and `weknora-root-t16-loader-1056f062-native.jsonl` in the same local Temp directory. These are native SQL-mock tests; actual PostgreSQL conservative fallback remains a whole-ticket gate. All root sessions collected. Loader slice accepted for assembly: main explicitly authorized to cherry-pick 0d then 1056; helper keeps clean freeze without repeated testing/ACK. Whole T16 is not accepted.

## Regression slice re-review — 8739d0cb

Verified clean `8739d0cbfb5a9610a7d6799ceb081d7fa767e9c9`, only new integration test file. The two previous fixture bugs are repaired. Independent Standards review reports 0 hard / 0 smells. Independent Spec review finds one P2 assertion gap: the module/src attempt/batch is now matched, but provider-call evidence remains a global counter. A different topic could call the provider while the module fails before its own provider execution, yielding a false positive. Root sent the same helper a minimal test-only repair to bind successful generation/calls and resulting new-snapshot applicability to the actual target module; failure recovery must not be substituted for this deterministic success path. Root integration-tag compilation PASS, package 4.231 s, **no tests to run**: compilation only, not PG behavior. No new helper PG run while main owns the slot. Main was told to wait for the reviewed test repair before assembly. Overall progress remains 18/22; no GitHub process publication or next-ticket dispatch.
