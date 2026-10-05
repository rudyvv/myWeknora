# T21 verification ledger

T21/#29 is in progress, not accepted. Approved review base is `c0747374a96908b98c29aaa49526d7dbd0ec48f9`. Contract and slice ownership: gitlab-code-wiki-rag-t21-coordination.md. Root is responsible for independent dual review and combined acceptance; no slice's partial green result closes the ticket.

## Model cancellation boundary (host, 2026-10-05)

Original T09/a8ea stable four-file work-in-progress window, before backend resource implementation. Worker Go build-cache access denial occurred before tests and is not behavioral red evidence. Root reviewed the test's first-provider counter reset and bounded waits, then ran actual native model-package tests without PostgreSQL.

`go test ./internal/models/chat ./internal/models/embedding -run 'TestConcurrency.*' -count=1 -json`: eight actual tests PASS; chat package 4.342s and embedding package 4.731s. Both new canceled-waiter provider counters PASS, with prior interactive-bypass, stream-release, background-limit, per-model-limit and pool fan-out regressions still green.

Root then overlaid only the two original `c0747374` production wrapper files through Go's temporary overlay, leaving the worker's files untouched. The same new tests actually failed: chat started a provider call after canceled semaphore waiting (package 4.185s); embedding canceled waiting did not return (case 2.01s, package 6.155s). This establishes an actual before/after behavioral regression, not an implementation-mirroring or compilation-only check.

Local bounded logs: Temp/weknora-root-t21-model-cancel.jsonl and Temp/weknora-root-t21-model-cancel-baseline.jsonl. Sessions 56201 and 7673 fully collected. Worker four-file mutation window released. No shared dependency restart, old container credential reads, or PG activity. Whole-ticket clean frozen review and resource/offline/fault/end-to-end gates remain pending.

## UI frozen candidate and repair batch

Root saved only the executor's ten owned frontend files as clean `29eeff256db41f21494271fe16ea58f91b7233f6` after the worker's shared Git index write was denied. Worker final Vue type-check PASS, diff check PASS; Node startup ENOMEM in the worker prevented local collection and was not counted as assertions.

Formal independent Sol/high axes versus approved c074 base: Standards 0 documented hard breaches, 1 nonblocking possible Duplicated Code judgement (four row-mapping shapes); Spec 1 P2 legacy compatibility defect. Failed historical records with a resolved commit_sha and absent optional detected/target fields incorrectly lose their known SHA. Published SHA must remain authoritative previous/publication evidence; do not use failed target as published. Publishing phase inclusion is a small compatibility consistency improvement, not an additional frozen Spec finding because the old backend does not emit that phase.

Root native component and sync-log tests collected eight cases on 29ee: 1 PASS, 7 failed before mounting due static vue-i18n import caching Vue runtime-dom before JSDOM globals. Exact fixture repair (defer i18n require after DOM) host-saved as clean `a90a759b7763fc79c78ea8c85f30e14d2e6f9818`. Actual rerun: 6 PASS/2 FAIL, 3384.6295ms; remaining failures are the real code-reading fixture's missing @/src alias resolution and an ungrouped numeric expectation despite actual zh-CN Intl formatting. Neither is silently called product red or passed. Root sent one precise combined repair batch for these two fixture defects plus P2 legacy target compatibility, retaining behavior assertions.

Native locale audit on frozen 29ee: 13/13 PASS, 4043.1099ms. Logs: Temp/weknora-root-t21-ui-native.txt, Temp/weknora-root-t21-ui-a90a759b.txt, Temp/weknora-root-t21-i18n-native.txt. All native commands completed. UI slice remains unaccepted; root and the same Luna executor handle corrections, with no next-ticket dispatch or GitHub process comment.

### UI slice accepted

Clean final UI 9d3be46f1c6919ac09d6ffe0bb2cbaa880dd5c46 preserves ten-file scope. Product repair at 44ebdff9df0e54243db3d7e12458dbacfd55045d passed independent narrow Spec review with zero remaining findings; Standards zero new hard breaches and one additional nonblocking possible test-loader duplication judgement. Prior row-map judgement remains nonblocking. Final one-line fixture expectation matches the existing locale; no product or locale behavior changes.

Actual final component and parent-log native run: 9/9 PASS, 3265.0237ms, log Temp/weknora-root-t21-ui-accepted.txt. The code-reading path, legacy target A/published B, observed zero and unknown telemetry, invalid metric handling and phase forwarding all reached their assertions. Locale 13/13 earlier result remains valid because locale files did not change afterward. Worker final typecheck PASS; host diff check and clean targeted commits confirmed. Final save command was initially run from frontend with a duplicated frontend path and did not stage anything; corrected root-directory targeted save preserved the exact tested one-line delta without rerunning unchanged tests.

UI slice accepted and merged into root, with frontend diff against the accepted branch empty. T21 whole ticket is still unaccepted (20/22 overall); no #29 closure, process comment or T22 dispatch. Backend and parser gates remain active.

## Pre-freeze root request-header counterexample (2026-10-05)

Independent native HTTP probe used the current parser Handler, one connection slot and a one-second header timeout. A client sent an unfinished header and then one byte every 0.3 seconds. Actual output after 2.094 seconds: `slow_peer_closed=false`; a second connection received `HTTP/1.1 503 Service Unavailable`. Thus the socket idle timeout does not enforce the intended finite absolute header-read duration. Root sent the exact evidence and requested an absolute request-line/header deadline plus a real slow-header recovery regression before parser freeze. This is not a formal whole-ticket acceptance or a parser build claim.

Probe: `C:/Users/28211/AppData/Local/Temp/weknora-root-t21-header-deadline.py`, using the existing Python and approved dependency path without credentials or source text. First probe sent a complete second request and hit Windows receive-side WinError 10053; revised second connection only read the server's admission response, producing the concrete result above. Both host commands ended; no test process remains. Parser's separately reported fault-only success does not cover this newly measured header gap, so a final full run after repair is necessary.

After the executor's absolute-header deadline repair, root's independent real Handler probe actually PASS: one-second configured header deadline, `slow_peer_closed=true` at 1.532 seconds and subsequent full health request `HTTP/1.0 200 OK`. Script `Temp/weknora-root-t21-header-deadline-fixed.py` tests closure and recovered public HTTP service, without invoking grammar or a provider. This closes the measured header counterexample at the pre-freeze stage; full frozen parser review and the executor's final bounded Docker suite are still pending. The host command completed and left no process running.

## Frozen parser slice review: 3c8f655b

Executor froze clean `3c8f655b9d713aa0cac651ce3ddf2f58b2c3b40b` (ten files, 1166 insertions/38 deletions); root resolved refs and nonempty three-dot diff against approved c074. Executor actual isolated HTTP suite: 89/89 PASS, 92.778 seconds, followed by seven fault/offline assertions under verified network-none/read-only/768MiB/2CPU/64PID limits. No native Go/PG/source publication or database-disk-full claim. Compose syntax check omitted a missing local .env and therefore does not verify real secret/env loading.

### Standards

Independent Sol/high: zero documented hard violations; two nonblocking possible Duplicated Code judgments (eight-limit defaults/mappings in server.py; repeated complete ParserLimits test literals). No tooling-only or Spec completeness findings mixed into this axis. Neither judgment requires a broad refactor.

### Spec

Independent Sol/high: two findings, worst P2. P2: disk-full fixture deletes the filler before parsing, contradicting the claimed request behavior while /tmp remains full. P3: deeply nested JSON can raise uncaught RecursionError and close the HTTP handler rather than return its newly documented bounded generic error; this input behavior pre-existed c074, so it is a contract mismatch, not a new parser-wide outage claim. Root confirmed real HTTP reachability at 3000 levels/6001 bytes under8192-byte transport limit: connection_closed_without_response, RecursionError logged=true. Initial1500-level probe received generic400 because that body decodes on the host Python3.12.14; it is not the counterexample. All probe processes completed; no provider, source repository or credentials were involved.

Root sent both required corrections in one repair batch to the same Luna/xhigh parser chat. Preserve ENOSPC through real parse/health assertions then remove the unique filler in finally; catch the JSON recursion failure generically with deep-input+subsequent-health regression. Production guard changes justify one final full HTTP/fault run, followed by narrow dual re-review. Parser slice and wholeT21 remain unaccepted; no integration/GitHub close/push/T22 dispatch.

## Backend frozen 39eb6f40 and helper gates (2026-10-05)

Root saved exactly 38 stable backend-owned files after actual worker Git denial, clean39eb6f407131abfbda636ba24c99bba2b80115b9. Fixed c074...39eb resolved/nonempty; parallel Sol/high whole review started. Helper clean39d434c701557a1d3b95e94259c2011dad06f143 static Standards0hard/0material and Spec0; host consumed only that test commit into backend candidate b37ce7d15c2d3fc25bc7d9e3c1ee35215c96b768. Product SHA remains39eb for frozen review; no root product integration or whole-ticket acceptance.

Actual native13 top-level tests PASS: source5.524s/config4.160s/types3.938s/embedding5.567s. Covers finite defaults/invalid budgets/admission cancellation/blob spool/cumulative transfer/tokenizer/telemetry validation/outbound HTTP retry and cancelled request; earlier eight shared governor tests remain valid. Session8242 collected.

First service command omitted integration build tag and returned no-tests4.633s; this is compilation only, not a gate pass. Corrected tags integration command reaches three real cases but ALLFAIL before behavior assertions with SQL42P01 source_cleanup_operations absent (package18.031s). Existing common java fixture only loads migration116 while previous T19 lifecycle tests separately initialize117. Root will return this precise common-fixture correction to main, using production migration rather than fictional schema. Sessions57437/37348 collected; PG57822 root exclusive, no running root tests.

Backend review confirmed measurable Wiki coverage JSON keys mismatch accepted UI, asynchronous coverage only captured at sync-time, and recovery counters partial; full findings batch is still being finalized before same-main repair dispatch. Parser two required repairs remain in original chat; first complete suite hit two timing failures, isolated repeat passes do not equal whole acceptance, final full/fault rerun is active. Original helper now owns only NEW source_quota_review_integration_test.go for retained original/cache/vector quota rejection and old-publication gates. This is same-ticket non-overlapping test assistance, not T22 dispatch.

### Backend whole review and single repair batch

Standards: 0 documented hard violations; 2 nonblocking possible Duplicated Code judgments (config/domain budget defaults-validation and staged/new index admission checks). No broad refactor required.

Spec: 2 P2 findings. Wiki coverage uses *_topics JSON names rather than approved short keys; asynchronous completed-plan coverage is captured only immediately after synchronous source publication, so no later UI/log refresh. Operational counters: durable lease recovery count never populated, and prior cleanup-residue count overwritten by current zero. Root sent one exact batch to original main, with JSON/read-scope/snapshot/recovery assertions and common real117 migration fixture repair. Whole T21 still unaccepted20/22. Existing helper owns separate quota regression file; no shared-file ownership conflict.
