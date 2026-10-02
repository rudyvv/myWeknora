# T15 db8a1d6c and 4861fb84 repair review

Targeted review of clean main repair `db8a1d6ccfa7b4c92cab6be8c45e741355a4d5f6` after `d406a67d21e4853ae4ea653a036492bf1aaf57b6`, plus only `SourceWikiModules.vue` and its test from UI helper `4861fb847d578c238249bb86a0ea412162ebd05a`. The approved full-ticket baseline remains `999e9398b8e4f67d17ba8e03c3fc65ba4b15332a`. These are repair slices of T15, not new accepted tickets.

## Standards

Independent Sol/high targeted review: **0 new documented violations, 0 new smell judgments**. The three previously recorded P3 suggestions remain nonblocking. Exact identity/range validation and terminal-parent-before-topic locking follow the agreed contract.

## Spec

Independent Sol/high targeted review: **0 new findings in these repair slices**. Late relation endpoints use bounded exact windows with snapshot, hash, UTF-8 and coordinate validation; required evidence exceeding the bound fails explicitly. Manual publication repairs matching failed initial coverage without changing terminal parent progress or consuming its budget. Failed system/flow retries use bounded batch preflight/start, staged candidates display as awaiting whole-batch QA, and the UI checks for an active batch before another start.

The separate causal-configuration finding remains unresolved until its producer, old-snapshot compatibility helper and main consumer are implemented, reviewed and independently verified. Passing these slices is not full T15 acceptance.

## Independent verification

- Five actual PostgreSQL tests passed in the initial focused run, including the root real-parser late Java-to-MyBatis counter (**7.21s**), native late-window and terminal-manual coverage tests, complete-set QA rejection/publication protection and manual expansion coverage. The package initially failed only because the old root manual fixture assumed a terminal parent's phase was `cards`.
- Corrected that root fixture to compare the actual parent phase/status before and after manual publication; the same actual PostgreSQL counter passed **5.61s**, package **10.006s**. This is a test-fixture correction, not an additional product repair.
- Root loaded the frozen helper component in the main tree's frontend harness: **6/6 actual Node/Vue tests passed**, **13.495s**, with `tsx --tsconfig tsconfig.app.json --test`. Earlier temporary-file placement and default-reference-tsconfig alias resolution failures were test setup issues. Temporary test files were removed.
- Both formal review agents were read-only. All root test processes were collected; the dedicated `127.0.0.1:57821/source_test` database was released to the main worker.

## Disposition

Main Luna/xhigh may cherry-pick only UI commits `eafd59de` and `4861fb84` onto its current repair, then run the composed frontend typecheck. The helper branch is not merged wholesale. Root has approved the config-evidence seam and resumed its original worker: typed exact causal fact refs, class-level Spring prefix where actually used, bounded fixed-snapshot validation/replay, and a distinct result for legitimate no-config matches versus failed/incomplete replay. The main worker waits for the exact exported interface before wiring that consumer.

**T15/#23 remains open, accepted count 17/22; T16 stays dependency-blocked.** No GitHub process comments or repeated same-SHA review were issued.
