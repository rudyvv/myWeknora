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

# Impact repair re-review — 8e51573c

Actual verified clean SHA: `8e51573c7ac4e2969609262ffa256e19b14e0bde` (worker corrected an initial full-SHA typo). Same approved baseline; repair changes only implementation and tests. Two independent Sol/high axes reviewed the complete slice plus the repair delta. The previous two findings are fixed. Root's original 14 focused tests and three independent overlay counters all PASS, package 4.580 s. The overlay replaces the old test file and therefore does not claim to run the worker's five newly added regressions.

## Standards

One new hard P2 remains: the new per-flow deletion proof at `source_wiki_impact.go:1367` walks every prior canonical-route entry for each omitted topic without charging the total work counter or caching the result. This violates the approved aggregate work contract. No other actionable smell.

## Spec

One new P2 remains: distinct accepted flow keys normalize to the same route, multiplying unchecked deletion-proof scans by topic count. The source-wide fallback promised for excessive total graph work is bypassed. Cache/precompute bounded route proofs or meter each scan and propagate exhaustion to clear partial plan results.

Root independently instrumented only the loop via temporary Go overlays: 10,000 `api_request` facts for one route and 1,000 whitespace-alias topics, all valid old dependencies, with a complete empty next snapshot. Actual result: **10,000,000 entry checks**, limit 2,000,000, disposition Planned, Removed 1,000, no error. Counter FAIL, package 3.842 s. This is operation-count evidence, not a timing threshold. Original source/test files remain untouched. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-impact-work-8e51573c.jsonl`.

Decision: small bounded-work repair sent to the same T06 Luna/xhigh worker; main may continue assembling/validating a candidate but must include the subsequent clean repair before final acceptance. No root integration, closure or next-ticket dispatch. Standards 1 / Spec 1, worst P2 in each axis; contribution slice remains accepted. Overall 18/22.

# Final impact slice acceptance — 9dc17a5b

Verified clean SHA `9dc17a5b1548ece8f50faee750470c89ad82104f`; same fixed point. The repair meters each flow-entry check, propagating exhaustion through `(bool, error)` into the existing atomic fallback. Both independent Sol/high axes re-reviewed the full slice and repair delta.

## Standards

Zero hard findings and zero actionable smells. Prior polarity and total-work findings are fixed.

## Spec

Zero actionable findings. Retained paths prevent false removal, equal/changed inventories behave correctly, and exhaustion clears all partial results.

Root rebuilt instrumentation overlays from **current HEAD**, avoiding the old frozen implementation overlay. Actual 20 focused tests (including all six worker regressions) plus four root counters PASS, package 4.831 s. The former 10-million-check input now stops after 1,976,997 entry checks, below the 2-million total limit, and returns `source_wide_stale / graph_work_limit_exceeded` with no Removed, Affected or Unaffected entries. Actual log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t16-impact-9dc17a5b.jsonl`. Root test session fully collected; no database/provider used.

Decision: impact slice accepted for clean candidate assembly; main notified to cherry-pick 9dc17a5b (already has the preceding repair). Contribution 36158f97 remains accepted. **Whole T16 remains pending** adapter, publication/recovery/auth behavior, independent PG verification and final dual-axis review. No issue closure or T19 dispatch. Totals at this HEAD: Standards 0 / Spec 0; overall 18/22.
