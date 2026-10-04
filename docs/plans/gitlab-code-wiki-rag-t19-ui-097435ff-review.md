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
