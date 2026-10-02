# T15 d406a67d review

Reviewed the clean full-ticket freeze `d406a67d21e4853ae4ea653a036492bf1aaf57b6` from user-approved `999e9398b8e4f67d17ba8e03c3fc65ba4b15332a`. The supplementary `3369e189...d406a67d` comparison isolates T15 from already accepted T17. The diagram helper `d7804929` is included as `e119dd01`; it is part of this ticket, not another acceptance milestone.

## Standards

Independent Sol/high review: **0 documented violations, 3 P3 judgments**.

- Duplicated request validation and HTTP error mapping in `internal/handler/source_wiki_batch.go:17–102`.
- Duplicated scoped-read initialization in `internal/application/service/source_wiki_batch_read.go:12–84`.
- Optional variadic diagram parameter in `source_wiki_attempt.go:664`, although production callers supply exactly one value; a required value would simplify the interface.

These are heuristic refactoring suggestions, not blocking repository rules. Parent-to-child transaction locking and diagram escaping follow the applicable domain/ADR conventions.

## Spec

Independent Sol/high review: **3 P2 findings**. The six findings from `90cb5d63` have repairs, including durable staging, complete-set QA, approval digest, absolute QA deadline, diagram integration, typed insufficient-evidence outcome, and manual expansion coverage.

1. **Flow evidence ignores exact late-file relation ranges.** Ticket acceptance line 14 requires verified static-chain evidence and diagrams when evidence is available. `source_wiki.go:176` limits each file to its first 2,048 bytes, ignoring `FromRange`/`ToRange`. The diagram helper then omits otherwise valid edges whose endpoints fall outside those excerpts. A root actual-parser/PostgreSQL counter confirmed a certain Java interface-to-MyBatis statement relation after byte 2,048, yet the collected evidence produces no diagram.
2. **Flow evidence omits causally required route configuration.** The same criterion explicitly requires configuration evidence. `internal/source/source_relations.go:587–646` can match routes through separate `api_prefix`/`api_proxy` facts, but `sourceFactRelation` retains only request/controller endpoints and an empty context. `source_wiki.go:115–141` selects only those endpoint paths. The card cannot cite the prefix/proxy configuration that makes the mapping valid. Verified by tracing the producer and its existing separate-file correlation fixture; no synthetic degraded-quality case is treated as a product finding.
3. **Failed batch-card retries are incomplete.** Parent spec lines 56/100 require bounded manual retries and ticket line 13 requires accurate coverage. `SourceWikiModules.vue:157` offers module retry for failed system/flow attempts with empty module paths, rejected by the module-only API. Root's actual Node/Vue counter captured that invalid request. Also, successful manual generation updates only expansion rows (`source_wiki.go:359`), so failed initial module coverage can remain failed. Staged attempts should be presented as awaiting batch QA, not failed.

## Independent validation

- Seven actual PostgreSQL tests passed, package **99.719s**: atomic parent/child reservations; at least twenty cards and competing QA runners; complete-set QA rejection preserving old body/version/source refs/revision count and releasing owners; page conflict rebase and complete-set revalidation within original child counters/deadline; fixed five-minute QA deadline; manual expansion coverage; stop/checkpoint and expired-parent recovery.
- Root's previous QA-rejection counter and typed insufficient-evidence producer test both passed, package **17.865s**. This confirms the old public-page leak was repaired.
- Actual diagram/skeleton focused Go tests passed, **4.777s**; `git diff --check` passed.
- Actual frontend `vue-tsc --build` passed. Native `tsx --test SourceWikiModules.test.ts` passed 4/5; the existing source-view test lacks a `SourceRegionBadge.vue` fixture import stub. That alias was already present in accepted `3369e189`; the helper executor is repairing the harness alongside the owned retry UI tests. The earlier attempted Vitest invocation was an incorrect test command, not a product failure.
- Late-flow root counter failed **9.92s**, package **14.186s**, at the final diagram assertion after real parser relation/range assertions succeeded. Earlier Controller fixture attempts did not produce a qualified edge and are setup failures, not additional findings.
- System-retry root Node/Vue counter failed at the explicit assertion forbidding an empty-path module request, **2.969s**.
- Failed-initial-module root PostgreSQL counter failed **6.37s**, package **10.816s**: the new manual attempt and published page are ready, while the authorized coverage reader still reports failed. The parent was terminal and no active batch counters were advanced.

The hypothetical `certain + degraded` diagram input was withdrawn as a blocking finding because the inspected current parser does not produce that combination for relation facts. No full application/container suite or unrun checks are claimed.

## Disposition

**Not accepted, not integrated, #23 remains open.** Existing accepted count remains **17/22**. All three original Luna/xhigh chats now have nonoverlapping T15 repair work: main b0df owns exact evidence windows and server coverage; 2119 owns retry UI/test harness; a8ea owns a proposed route-configuration causal-evidence helper, preserving its accepted T11 branch. That helper's contract must be approved by root before implementation. T16 stays dependency-blocked until the complete T15 repair freeze passes review and independent verification.
