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
