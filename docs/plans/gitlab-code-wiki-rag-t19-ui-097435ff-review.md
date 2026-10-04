# T19 UI review: 097435ff

User-approved fixed point: `e2677b62e8d847d2585f103d634f4945c0839d50`. Frozen candidate: `097435ffc8ad1cc81707ec795f051fa770fb9733`, branch `codex/t19-source-management-ui`, original UI worktree `C:/Users/28211/.codex/worktrees/2119/WeKnora`. Root saved exactly eight assigned frontend files after the worker's one Git index permission failure; no dependency, backend or migration file changed. Working tree was clean before review. Comparison: `git diff e2677b62...097435ff`; one commit `097435ff feat(source): add lifecycle management controls for T19`.

## Standards

Independent Sol/high review: **0 hard violations, 1 non-blocking Fowler judgement**. Possible Duplicated Code / Middle Man at `DataSourceSettings.vue:338–344`: edit and sync permission wrappers have identical expressions around the existing connection guard. This does not block acceptance or justify a cosmetic refactor during the behavioral repair.

## Spec

Independent Sol/high review: **1 P2 finding**. `loadList` applies every GET without checking whether it predates a lifecycle mutation. An old list response may arrive after an accepted clear and its new list response, restoring the old bound/query-enabled card and controls. The approved contract requires authoritative lifecycle replies and no local restoration of cleared visibility. A request/mutation generation guard must discard stale responses, including when the post-mutation refresh fails.

Root independently reproduced the exact ordering with the real compiled Vue component, a delayed existing list API stub and the normal locked tsx runner. Start silent sync refresh GET A, accept clear, allow fresh GET B to render query-disabled, then release A's old bound reply. The assertion that query-disabled remains visible **fails** (case 332.6305ms, package 2230.3085ms). Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-ui-counter.txt`; isolated root test artifact is ignored under UI `frontend/node_modules/.cache/t19-root-review/management-counter.test.ts`. Frozen product and worker test files were unchanged by this counter. An earlier counter using pause blocked before the target assertion because loading hides the card; that experiment is not product-failure evidence.

## Validation and disposition

The six ordinary management tests on the corrected selector fixture actually **pass**, zero skipped/fail, 4170.3968ms. Log: `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-ui-current.txt`. Worker Vue type-check actually passed 29.907s. Pre-selector-fix three menu failures remain historical evidence; neither those old failures nor the worker's tsx startup `uv_os_get_passwd` error describe the current six-test result.

Candidate **not accepted or integrated**. Root sent the P2 and actual counter to the same Luna/xhigh UI chat for a minimal guard and real delayed-response regression. T19/#27 stays open; overall acceptance remains 19/22. Backend implementation continues separately; T21/T22 remain blocked. No PostgreSQL, shared service, GitHub process comment or permission-policy change was needed for this review.

## Repair 9b955cee and residual mutation ordering

Worker froze two files after a minimal request-generation guard and deferred-list/failing-refresh test; host saved clean `9b955cee602c2f8e16135aaf4ea8107722388e38`. The new regression actually failed before the guard (335.1673ms, package 2511.3015ms) and the normal seven management cases subsequently PASS **2597.3663ms**. The original independent delayed-GET/successful-refresh counter PASS **2345.4124ms**, while worker vue-tsc PASS **29.075s**. The stale-GET P2 is resolved. Narrow Standards: 0 new hard violations/0 new smells; earlier wrapper judgement remains non-blocking.

Narrow Spec found a different **P2**: overlapping lifecycle POST replies are not ordered. While unbind waits for its reply, clear remains available. If unbind commits first but its reply arrives after accepted clear, `refreshSourceLifecycle` applies its older query-enabled DTO; a failed following GET leaves that state visible. Root independently reproduced this on clean 9b using the actual component: deferred old unbind reply, successful clear plus its GET, then failing refresh on release of unbind. Target assertion **fails** (337.2557ms, package 1899.9265ms), log `C:/Users/28211/AppData/Local/Temp/weknora-root-t19-ui-mutation-counter.txt`.

Root directed the same UI worker to serialize lifecycle writes per source, with reactive pending controls and a deferred-mutation regression. It must retain independence for other sources and the resolved list guard. Candidate 9b is **not accepted/integrated**. This second actual failure is not a rerun of the resolved first finding. No extra ticket, permission question, alternate runner or GitHub process record was introduced.

## Final UI repair ebec7cc2 accepted into the T19 candidate

Clean UI freeze `ebec7cc223c9bf8b2c8576e44ad403c83b9d07cd` serializes mutations with a reactive per-source pending set and releases it in `finally` after authoritative replies and refreshes. Other sources remain independent. The earlier GET-generation guard is preserved. Root's eight normal management tests PASS **2819.9504ms** (`weknora-root-t19-ui-serialized.txt`), and both independent ordering counters PASS **2241.4019ms** (`weknora-root-t19-ui-both-counters-green.txt`). Worker Vue type-check PASS **30.489s**. A separate run whose filename ends in `mutation-regression-red` actually ran after the guard and passed; it is not counted as pre-fix failure evidence.

Independent Sol/high narrow review after recovery: Standards **0 hard violations, 1 new non-blocking possible Duplicated Code judgement** for the three mutation lock/release shapes; Spec **0 findings**. The earlier wrapper judgement remains historical and is not counted again. Both actual P2 ordering failures are resolved.

Root accepted this UI slice and cherry-picked its three commits into the main T19 candidate as `1e69b497`, `db1fca60`, and `bc6987cb`. The candidate's frontend diff against frozen `ebec7cc2` is empty. This is candidate integration, not publication or whole-ticket acceptance: backend migration, cleanup, read-revocation and recovery gates still need real verification. The root integration product remains at accepted `e2677b62`; #27 stays open, acceptance remains **19/22**, and T21/T22 remain blocked.
