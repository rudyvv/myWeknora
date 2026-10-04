# T16 pure slices review — 2026-10-04

Approved fixed point: `55d69df1355fba044ac0e58bc8f2f4dc2c15d2c0`.
Scope: impact `422525cc0a0d5b53504dd61cf76cb6b8a9db649f` in b0df; contribution `36158f97dc0f162319576ba04d8a1e149475d4e5` in flow-config-diagram. Both HEADs verified clean; each changes only its three owned pure implementation/type/test files. Diff commands: `git diff 55d69df1...422525cc`, `git diff 55d69df1...36158f97`. Main adapter remains work in progress and is outside this review.

Two independent GPT-6 Sol/high agents reviewed Standards and Spec. Spec: ticket 16 and the recorded 2026-10-03/04 pure-module contracts. Standards: repository instructions/domain docs/ADRs, approved contracts and the code-review skill's Fowler heuristics.

## Standards

Impact: **one hard finding (P2)**. `internal/source/source_wiki_impact.go:277` treats `sameSourceWikiImpactModuleInventory` as a changed flag, although the helper at line 1395 returns true on equality. This violates the approved comparison contract: identical module membership spuriously rescans, while membership-only changes can miss the required skeleton rescan. Invert the result or implement an explicitly named changed predicate; test both cases. No further actionable smell reported.

Contribution: **zero hard findings, zero actionable smells**. Exact source/topic targeting, bounded results, evidence checks and copied outputs match the pure-module contract.

## Spec

Impact: **two P2 findings**.

1. Inverted module comparison described above violates T16's complete module-membership impact requirement.
2. `sourceWikiImpactRemovalProven` at line 1337 can withdraw an existing source-owned module when the next manifest still retains its parsed, structural source path but facts do not qualify as module candidates and the next topic/module inventory omits it. Topic omission and missing candidate facts do not establish true source deletion. Retained dependencies must remain affected/stale unless complete evidence proves their removal.

Contribution: **zero findings**. Persistence, authorization, rendering, proof behind Unchanged/ConfirmedDeleted and actual publication behavior remain the main adapter's responsibility; this slice alone does not satisfy the complete ticket.

## Independent verification

- Impact's 14 existing top-level focused tests: PASS, package 4.635 s; exercised old/new reverse relations, version rotation, uncertain relations, parser/member changes, bounds and identity failures.
- Contribution focused tests: PASS, source package 4.175 s. Types package reported no matching tests and is not counted as assertion evidence.
- Root used a temporary Go overlay, leaving helper files unchanged. Stable inventory incorrectly requested rescan; inventory-only change incorrectly omitted it: two FAILs, package 3.781 s.
- A first removal fixture changing recognized Service facts hit the earlier safe public/config fallback, so it is not evidence of direct withdrawal. Root corrected the reachable fixture: old/new `app/Helper.java`, same SourceFileID/content/structural `java_type Helper`; old published source-owned `module/app` and complete old membership, next complete inventory omits the topic/module while retaining the source. Planner actually returned Removed: FAIL, package 1.910 s.
- Temporary local evidence: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-impact-counter_test.go`, `weknora-root-t16-impact-counter-overlay.json`, `weknora-root-t16-impact-counter-422525cc.jsonl`, `weknora-root-t16-impact-retained-422525cc.jsonl`. No source repository/provider/database accessed by these pure checks.

Decision: contribution slice accepted for assembly into the candidate only. Impact rejected; concrete findings dispatched to the same T06 Luna/xhigh chat, main notified to consume the eventual clean repair. No root product integration, GitHub closure, T19 dispatch or complete T16 acceptance. Overall accepted progress remains **18/22**.

Totals: Standards impact 1 (worst P2), contribution 0; Spec impact 2 (worst P2), contribution 0.
