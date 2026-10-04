# T16 1a2de6ba review

Approved baseline: `55d69df1355fba044ac0e58bc8f2f4dc2c15d2c0`. Frozen production candidate: `1a2de6bab0bf91442378d729004a65e118b7e9b6`. Root committed the execution chat's frozen six service files and previously reviewed regression file after host-only validation/commit handoff. Test-only follow-up `a6c5ab2f80f35b1ed1f0efc53a6f8493eb62022e` copies helper `65492277a69785a109a1b2656e0af92f770c6205`; production is identical.

Not accepted or integrated. T16/#24 remains open, total accepted 18/22. No subsequent ticket dispatched.

## Standards

Two hard findings from independent GPT-6 Sol/high review:

1. P1: mixed replacement registers A and B's contribution-local evidence IDs in one page version, while migration 000105 makes `(page_id,version,evidence_id)` unique. Two independent cards legitimately use `e001`; the complete mixed write rolls back. Read-lease evidence ownership has the same insufficient namespace. This violates the independently attributable contribution contract in CONTEXT.md.
2. P2: generic page revision registration preserves only primary page provenance evidence. Archived mixed body/contribution B loses its revision raw owner after all current owners are replaced. ADR 0007 requires raw evidence ownership for every retained body.

One Fowler judgement: possible duplicated validation/projection between replacement and deletion. This is not a mandatory refactoring or an additional hard blocker.

The previous JSON null arrays and undispatched deterministic fallback are fixed.

## Spec

One remaining P2 from independent GPT-6 Sol/high review: source-sorted mixed projection can make B the page primary after replacing A. The next A generation rejects any page with another primary source before consulting A's independently attributed contribution (`source_wiki_attempt.go:379`). That prevents consecutive A updates required by ticket lines 13 and 16. A matching, validated contribution must authorize the targeted replacement; page-level primary cannot stand in for sole ownership.

The prior null-array, fallback, and actual written-version failures are addressed. No publication-wide budget requirement was added.

## Actual validation and diagnostic limits

Root ran six actual PostgreSQL/parser integration gates at the frozen production candidate, package 90.365s:

| Test | Result | Time |
| --- | --- | --- |
| Incremental regeneration to exact target attempt/ready page/current contribution | PASS | 24.40s |
| Helper oversized-fact durable fallback | PASS | 11.32s |
| Helper attributable mixed deletion | FAIL: page not found | 13.39s |
| Affected publication durable handoff | PASS | 10.16s |
| Oversized-fact pending items terminal | PASS | 12.11s |
| Main attributable mixed deletion | FAIL: two current rows remain | 13.51s |

Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-1a2de6ba-pg.jsonl`. Test-only mixed replacement at a6 failed 45.82s (package 50.083s), before the target A attempt existed, with repeated target-page-version fence deferral. Log: `weknora-root-t16-65492277-mixed-replacement-pg.jsonl` in the same Temp directory. Therefore the unique-ID finding is currently code-confirmed, not claimed as the observed SQL cause of this run.

A fixed a6 isolated diagnostic repeated the mixed failure (48.66s, package 53.575s). A's current plan has skeleton completed, module/system regeneration running, and expected version 1; its stored shared page is also version 1. B's delayed initial publication is processed with no predecessor and marks its existing B contribution stale. Log: `weknora-root-t16-a6-mixed-plan-diagnostic-pg.jsonl`.

Root traced two relevant seams for repair, rather than guessing real concurrent edits:

- The read predicate requires every source reference to occur in primary provenance evidence. A valid independently attributed B reference cannot satisfy that A-only array. Typed mixed reads must validate the complete registered contribution evidence set, keeping permissions, immutable raw hashes, coordinates, historical owner binding, and the existing answer-ready publication rules.
- Deletion projection currently requires target snapshot = old applicability for every row. Publication fencing intentionally gives removed A a new target snapshot while retaining old evidence/applicability. The removed target must be distinguished from unchanged survivors; do not relax foreign contribution validation.

The focused test fixture also reparented B's contribution without registering B's exact raw owner under the shared page, and left initial publication deliveries until after generating cards. The narrow helper review found Standards 0 hard / 1 possible duplication smell, Spec 1 P2 about event isolation. Test repair must register B's complete shared-page owner after the new namespace migration and consume initial publications before constructing the focused two-update case. Delayed initial notifications affecting already-current ready contributions remain a separate legitimate service timing concern to investigate, not evidence that the focused replacement assertions passed.

## Parallel repair ownership

- Main original T09 chat / a8ea / `codex/t16-wiki-incremental`: service consumers, exact contribution generation permission, removal proof distinction, notification timing/fence outcomes, and its own integration fixture migration load.
- Original T06 chat / existing `t16-impact-loader-errors/WeKnora` / `codex/t16-mixed-evidence-owners`, from a6: repository evidence registration, typed mixed read predicate, exact owner/read-lease identity, new migration 000116 and repository tests. Accepted loader 1056 and its former branch remain unchanged. No service/115 edits.
- Original T10 chat / flow-config-diagram / `codex/t16-incremental-regression`: only its new regression test file, initial-event isolation, complete B owner setup, deterministic B-primary input, and consecutive A updates. No production edits or PG.

All executors remain Luna/xhigh. Root retains planning, contract decisions, Sol/high formal review, actual database slot and host commits. All root sessions 57917/41439/30943 are collected. No shared dependency, old container credential, or original main checkout was touched. Only local review records were updated; no GitHub progress comments.

Standards: 2 hard findings, worst P1, plus 1 judgement. Spec: 1 finding, worst P2.
