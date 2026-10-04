# T16 mixed-evidence slice review

Approved whole-ticket baseline: `55d69df1355fba044ac0e58bc8f2f4dc2c15d2c0`. Repository slice: clean `6e5a86454e005816236f330652fbf22cbf7c7122`, compared with `a6c5ab2f`. Root assembled it with reviewed service `2cc0d7b5`, regression `92c8e41a` and migration fixture `6fa327ed` as clean candidate `6f4108f2970aaf21ecff2ce3eb302871acfa711d` in a8ea. This is a review candidate, not an accepted integration.

## Standards

Independent Sol/high review: 2 hard findings, worst P1; no additional actionable Fowler judgement.

1. P1: `readWikiPageDB` attaches the strict typed mixed gate whenever the contribution table exists, even for single-source pages. Migration 115 backfills older single-source pages/revisions with unattributed-body markers. Those pages previously passed their exact primary raw-owner/permission read rule and now become unreadable. Only actual multi-source projections should require the mixed gate; multi-source pages without typed tables must continue failing closed.
2. P2: migration 116 checks current `wiki_pages.source_refs` when backfilling archived contribution evidence. A source retained in an older mixed revision but subsequently withdrawn from the current page is filtered out. ADR 0007 requires a raw owner for every retained body; revision rows must use exact revision refs.

## Spec

Independent Sol/high repository review: 2 findings, worst P1.

1. P1: mixed evidence registration is checked with `SnapshotSQL` against each evidence's **origin** snapshot. After A republishes, the authorized read lease pins A's new publication while the stale card and historical revisions correctly retain old raw evidence. The gate hides the card from regeneration and hides history. Registered raw owners require exact immutable provenance, published retained snapshots and current permission; the separate answer predicate still requires ready contributions and current applicability. T16 lines 13/16 and ADR 0007 distinguish these contracts.
2. P2: the historical backfill/current-ref mismatch described above also leaves retained revisions without the evidence owners required by ADR 0007.

## Actual PostgreSQL validation

Root ran seven gates at fixed assembled `6f4108f2`, using only the dedicated localhost 57822 database and safe in-process runner. Package failed in 144.763s; session 34064 is fully collected.

| Gate | Result | Time |
| --- | --- | --- |
| Exact module regeneration/current contribution terminal outcome | FAIL: no own target attempt | 44.96s |
| Helper oversized-fact durable fallback | PASS | 10.98s |
| B-primary consecutive A replacements | FAIL: own target attempt timed out | 43.33s |
| Helper complete-manifest mixed deletion | FAIL: resulting page not found | 11.43s |
| Affected publication durable handoff | PASS | 8.89s |
| Oversized-fact pending items terminal | PASS | 10.13s |
| Main attributable mixed deletion | FAIL: both rows remain | 10.77s |

Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-6f4108f2-pg.jsonl`. These failures are not reported as SQL local-evidence-ID collisions. The widened keys and exact registration are present, but read/transition blockers prevent the end-to-end assertions from passing. Historical backfill and legacy regressions remain code-confirmed at this freeze; this gate set did not simulate an existing pre-116 retained mixed revision.

## Targeted service follow-up

Root traced the remaining deletion path and received an independent Sol/high Spec confirmation: publication fencing marks A's current row stale and updates page-primary state, while leaving `contribution.source_provenance.state` ready. The removed-topic branch calls deletion projection before the stale-marking helper that would synchronize the JSON. The projection rejects row/provenance disagreement; for A-primary pages, its final provenance equality also fails. The result is `removal_not_proven` despite complete absence proof (T16 line 15). This targeted service follow-up has 1 Spec finding, P1; it is separate from the repository slice's two findings.

Root assigned the main executor a non-conflicting additional owned file, `repository/source_wiki_update.go`, to keep publication-fence state and valid typed provenance state consistent in the same transaction. Legacy/untyped rows must not acquire fabricated provenance. Survivor evidence/permission and deletion proof checks remain strict. Repository helper owns only `source_wiki.go`, `wiki_page.go`, its new test and migration 116; it is repairing the three repository behaviors above. The regression helper remains frozen and idle after its slice passed, awaiting actual assembled validation.

T16/#24 remains open; 18/22 accepted. No next ticket, integration, GitHub progress comment or shared-service restart occurred. Root owns the PostgreSQL slot; workers do not need user-provided credentials or runner paths.

Repository slice: Standards 2 hard, worst P1; Spec 2, worst P1. Targeted service follow-up: Spec 1, P1.

## Repair review and assembled validation

Repository repair `8dc71786eb3cf683b9634ee5597aa35ff7e39ad1` fixes the three repository behaviors above. Independent narrow Sol/high reviews report Standards 0 hard / 0 actionable smells and Spec 0. Its focused native tests passed in 3.549s; the migration assertion in those tests inspects SQL and is not a database backfill test.

Publication-fence repair `339dafc28990a19ac0063ab68170bfae84408b4c`, compared with `6f4108f2`, synchronizes valid typed JSON provenance from ready to stale in the same transaction as the row. Independent Sol/high narrow reviews report Standards 0 hard / 0 actionable smells and Spec 0. Original evidence/applicability snapshots and foreign contributions are unchanged.

Root assembled both repairs as clean `0ed519867d80d5212bad97261698cc14e92d8343`. Actual seven PostgreSQL gates ran once after these changes, package 111.410s, session 78797 collected. Six passed: exact module terminal regeneration 17.77s; helper oversized fallback 9.20s; helper attributable mixed deletion 11.05s; durable handoff 8.65s; main oversized terminal fallback 8.31s; main attributable mixed deletion 10.04s. B-primary consecutive A replacements still failed in 41.68s while waiting for the first A/module/src own ready attempt. Model output showed system generation, not a module generation call. This remains unaccepted and has been assigned to the main executor for read-only diagnosis while root keeps the candidate fixed.

Actual log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-0ed51986-pg.jsonl`.

Root also created a temporary Go overlay (outside Git) with a legitimate archived A+B revision, current B-only page, two sources sharing local `e001`, and only the historical primary B raw owner before backfill. Executing the old frozen 116 SQL against the dedicated fixture schema reproduced the missing historical A owner: expected 1, actual 0; test FAIL 11.67s, package 15.940s. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-history-old.jsonl`. The corrected migration, exact historical raw reads/read pins and legacy single-source readability are under actual validation; they are not claimed passed here.

T16 remains open and unaccepted; 18/22 accepted. The repair reviews are slice reviews, not whole-ticket acceptance.

## Actual historical and remaining-target diagnostics

The same isolated archived A+B/current B-only fixture passed with corrected migration 116 in **10.46s**. It executed the real migration SQL, then read both exact historical raw versions, verified their source/file/SHA identities, and confirmed two lease pins coexist under the same local evidence ID. It also verified a legacy single-source page remains readable with migration-115's unattributed-body marker. Old SQL fails this same fixture at the missing-A-owner assertion; this is a behavioral red/green result.

The temporary overlay also extends the consecutive-update test with historical mixed raw reads after both publications, but execution has not yet reached those assertions. Its current failure (45.93s; combined package 60.909s) yields concrete durable diagnostics: the expected page ID/version is correct, but generation lookup returns no page/base version 0; the batch and module/system items settle failed, while skeleton inventory settles completed. The cause is generation's A-only source-read lease hiding a valid A+B projection. The correct repository whole-body read gate must remain strict. Root approved a minimal generation projection lease change to the already-authorized KB; new evidence remains explicitly A/snapshot scoped and `BeginSourceRead` must retain existing caller source/file/tag scopes instead of replacing them. The original main Luna executor is implementing this last correction; no result is claimed for it yet.

Native focused source/planner/contribution checks passed in 4.474s and repository mixed/evidence checks in 3.820s. The types package reports no matching tests (3.932s), so it is compilation only. Root sessions 80634 and 19996 are collected.

## Generation lease repair and adjacent metadata regression

Root saved the main executor's one-line generation lease correction as clean `2762998e69605fc5a5268af64f9946fc1aba8e8a`. Narrow independent Sol/high Standards reports 0 hard / 0 new actionable smells; Spec reports 0. The worker's compile-only attempt failed once on Go build-cache access and was not retried. Root actual PostgreSQL compilation/execution succeeded.

At that freeze, consecutive A replacements **and the added exact historical A+B raw-read assertions PASS 23.75s**. Generation publication/CAS guards PASS 10.95s (model parameters, synthesis model, source config, page version and publication subcases), all read projections rejecting corrupted raw PASS 5.17s, filtered historical files/explicit clear PASS 10.44s. Session 7969 is collected, combined package 63.257s.

The pre-existing whole-page/history scope test reached a new positive-path failure in 8.37s: after changing only `PageType` to summary (source-bearing title/body/summary/aliases/refs/provenance unchanged), the all-source read tool returns an empty result. Its negative file/tag-scope checks passed. `UpdateWithRevision` advances the page version and unconditionally marks contributions unverified, while page-primary provenance remains ready. The strict answer gate correctly rejects that inconsistent projection. An independent Sol/high Spec inspection confirmed **1 P2 product regression**, not an obsolete test expectation (CONTEXT.md source applicability definition). The read gate must remain strict.

Root assigned the same main executor a small non-conflicting repair in `repository/wiki_page.go` and `repository/source_wiki_update.go`; the repository helper is frozen. Only exact source-bearing old/new identity equality permits contribution version carry-forward without state/proof changes. Prose/title/summary/alias/ref/provenance mutations keep unverified invalidation, and stale or wrong-version rows must never be promoted ready. Archival, current raw-owner registration and optimistic page CAS stay in the same transaction. This follow-up is not yet accepted; root will collect its clean host freeze, narrow reviews and real existing regression.

Actual log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-2762998e-pg.jsonl`. Root overlay is outside Git and does not modify the worker's regression file. No GitHub process publication or next-ticket dispatch occurred.

## Metadata repair result and GC fixture timing

Clean `6216b7b3445740603ea900c2c42b9ef05812af33` implements the exact-projection version carry rule in three files. Root focused predicate/mutation/identity unit checks PASS 3.505s. Independent narrow Standards: 0 hard / 0 new actionable smells. Independent narrow Spec: 0 findings; reachable service callers derive the old revision from the current page, and the branch does not promote any row's state.

Actual adjacent PostgreSQL gates at 6216: typed atomic publication/outbox PASS 10.73s; consecutive A replacements plus historical raw assertions PASS 18.14s; unaffected applicability carry PASS 11.05s; whole-page/history source/file/tag scope **PASS 7.49s**; bookkeeping cannot forge evidence/machine edits PASS 5.67s; ordinary-document/source mixed scope PASS 7.06s. Original GC/pin test initially FAIL 8.36s because its newly added T16 old→new impact plan was still pending, so GC correctly retained the old manifest/index needed for impact proof. Package 72.672s; session 50297 collected.

Independent Sol/high Spec inspection confirmed that GC failure is fixture timing, not a product finding. The fixture must consume the real update lane before asking GC to discard impact-proof inputs. Root temporary overlay does exactly that, asserts no pending/running plan remains for the old snapshot, then runs all original pruning/read-pin/index-removal/raw-retention assertions: **PASS 12.84s**, package 17.226s, session 20396 collected. No production GC logic or artificial plan-state flip was used. The main executor is applying only that short fixture sequencing adjustment to the existing test.

Logs: `weknora-root-t16-6216b7b3-pg.jsonl` and `weknora-root-t16-gc-lane-pg.jsonl` under the same root Temp directory. The first run remains recorded as failed; only the calibrated real-lane run is reported green. Final assembled acceptance/integration is still pending.
