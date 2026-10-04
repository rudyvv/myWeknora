# T16 d8e9a4d6 review

- Approved baseline: `55d69df1355fba044ac0e58bc8f2f4dc2c15d2c0`.
- Frozen candidate: `d8e9a4d61165b16d0931d6efa357ac2101d6ade9`, main worktree `C:/Users/28211/.codex/worktrees/a8ea/WeKnora`.
- Two independent GPT-6 Sol/high reviews; execution remains in the original GPT-6 Luna/xhigh chats.
- Not accepted, integrated, or published. T16/#24 remains open; total accepted remains 18/22.

## Standards

Two hard P2 findings, no Fowler judgement findings:

1. Empty topic relations/uncertainty arrays serialize as JSON `null`. Migration 000114 requires arrays. The ordinary generated skeleton cannot be persisted (`source_wiki_update_generation.go:277-291`).
2. Typed deterministic loader fallback persists a completed plan but returns before dispatching/settling its skeleton item. Recovery selects pending regeneration items only; the lone skeleton remains pending indefinitely (`source_wiki_update_processor.go:113-146`, `source_wiki_update_generation.go:474-478`). This violates durable recovery requirements in ADR 0010.

## Spec

Three findings:

1. P1: the null relations failure prevents ordinary affected-topic regeneration. Root PostgreSQL execution confirmed the constraint violation.
2. P2: deterministic fact overflow leaves one pending skeleton item after delivery acknowledgement and plan completion. Root PostgreSQL execution confirmed the incomplete outcome.
3. P2: replacing A on an attributed A+B shared page builds an A-only body, carries old references, and writes only A while the generic update invalidates all current contribution rows. B's body/current attribution is lost. The code path is reachable in `source_wiki_attempt.go:692-735`, `source_wiki_batch_worker.go:754-772`, `source_wiki.go:637-655`, and `repository/wiki_page.go:159`. T16 ticket line 13 requires replacement to affect only the matching source. Actual PostgreSQL replacement regression is being prepared; this finding currently has code evidence, not a claimed runtime reproduction.

The former missing generation dispatcher is now connected. Attributable deletion projection is implemented, but its current runtime test stops at the earlier skeleton constraint failure. The budget concern was withdrawn: approved limits are per attempt/T15 batch; no publication-wide quota was approved. Do not introduce that new requirement during repair.

## Independent validation

Root ran the candidate's three integration tests against the dedicated `127.0.0.1:57822/source_test` database and real parser HTTP process. Dependency preflight and PostgreSQL readiness passed. Results:

| Test | Result | Evidence |
| --- | --- | --- |
| `TestSourceWikiAffectedPublicationHandsOffToBoundedGeneration` | FAIL, 14.70s | SQLSTATE 23514, `source_wiki_topics_relations_check` |
| `TestSourceWikiOversizedFactsPersistFallbackWithoutRetry` | FAIL, 10.36s | completed source-wide fallback leaves pending item count 1 |
| `TestSourceWikiConfirmedDeletionPreservesOtherAttributedContribution` | FAIL, 10.73s | same relations constraint before deletion assertions |

Package duration: 40.482s. Complete log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-d8e9a4d6-pg.jsonl`. All root sessions were collected. These are actual product failures, not missing-parser setup failures.

Root assumed host-only commit/PG operations after the execution chat reported environment restrictions, preserving its dirty implementation rather than asking the user for internal runner paths. The candidate contains the accepted loader's exact three files at `1056f062e86f3c23d74c5c20fb828b1a50e4166d`, including repository tests. Safe runner is dot-sourced only; no credential contents or old container metadata are read or published. Shared services remain unchanged.

Regression helper commit `d3cfcfde9272663491f3dbc4df78e4c37f6cbb72` has separate Standards 0 / Spec 0 static review. Its one new integration test file is staged for assembly in the main tree, currently untracked, and has not yet passed actual PostgreSQL validation. A targeted A+B replacement test is assigned to the same helper. Main owns production repairs; root owns actual PG and host commits. Neither slice completion nor unit/compile results constitute whole-ticket acceptance.

Standards: 2 findings, worst P2. Spec: 3 findings, worst P1.
