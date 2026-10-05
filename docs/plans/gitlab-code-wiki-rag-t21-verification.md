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

## Parser repair accepted: 8b14c1ee

Clean8b14c1ee11dd00c5d7a2c35826bf036eab2f735a changes four files within owned ten. Independent narrow Standards0newhard/0newsmells and Spec0remaining; prior two nonblocking judgments stay unchanged. Executor final90HTTP plus seven actual bounded-Docker fault assertions PASSexit0, after a first suite with two intermittent timeouts and isolated passes; no assertion weakening or runtime lock bump. LinuxPython3.12.14 accepts3000-levelarray so that container case reaches generic validation, not the RecursionError branch.

Root native Windows previously failing actual Handler counter now returns400generic, RecursionError not logged, subsequenthealth200readytrue for6001bytes under8192budget, confirming the actual branch fix and slot recovery. First host rerun omitted existing dependency PYTHONPATH and failed import before behavior; corrected environment used existing approved dependencies without reinstall. Native fixed script Temp/weknora-root-t21-nested-json-fixed.py completed. Root merged accepted parser slice as5d079718fbbf207c80fb8f609318fe250dbbb513; wholeT21 stillunaccepted.

Root built exact accepted image weknora-source-parser-root-t21:8b14c1ee (sha2561d3ceb5f...) and started root-owned weknora-source-parser-root-t21 on root-owned internal networkweknora-source-parser-root-t21-net, loopback57823 only. Verifiednonroot65532/readonly/805306368memory/2CPU/64PID/noaddedcaps/no-new-privileges/tmp32MiB for upcoming whole Go/PG gated paths; no shared app/dependency changes. This is a running test resource, not a source end-to-end gate pass. Build session53477 collected. Root must remove only these explicitly owned resources after combined tests. Backend/helper implementation remainsactive; PG57822rootexclusive and idle. NoGitHubprocesspublish/T22.

### Independent quota candidate and bounded parser connectivity

Helper clean91750f1cd491769a7412255c8166c7671ff54b0c adds119lines one reserved testfile. Narrow Standards0hard/0judgments and Spec0. Root preserved main active tree by consuming frozen backend39eb into already-owned idle helper2119 branch as clean55bb303fbed829a829186525613d8dcdf088f2f7; this is an independent verification candidate, not accepted product integration. Three new quota subcases use real publication, measuredusage+1, original/cache/vector transaction rollback and retained sourceA+B evidence. Actual tagged PGtest running26920 against root57822 and rootlimited parser57823; no pass claim yet.

Internal-network-only parser was running/ready via dockerexec but Docker Desktop refused host loopback connection. Root attached only the root-owned test container to Docker bridge; actual loopbackhealth200ready succeeded, all process/cgroup/read-only limits unchanged. The Go/PG test now reaches the accepted image through loopback; this combined native-client test does not claim network-none isolation. The executor's independent network-none90HTTP+faultsuite is the offline evidence. Root test container/network/image names are recorded above for exact cleanup; no shared dependency changes or credential inspection. First failed hostprobe is an environment limitation, not parser failure.

### Actual quota and rejection/cancellation results

On independent clean55bb candidate, actual tagged quota test PASS35.306s: original13.69s/cache8.52s/vector8.21s; all attempted over-quota payloads rollback, prior sourceA evidence and independent sourceB remain readable/searchable. Uses root57822 and accepted resource-limited Dockerparser57823, no fake database schema. Log Temp/weknora-root-t21-quota-docker-pg.txt. Session26920 collected.

Root overlaid only common test fixture to call existing ensureT19LifecycleSchema with the real117 migration, leaving candidate files untouched. Corrected fixture gates actually reached assertions: selected-byte rejection/sharedB+ordinarydocument scope PASS11.68s; pending-admission cancellation/provider-not-called/redelivery PASS6.13s. Coverage case FAILsetup3.43s at repeated production115 migration (42P07 existingtable), package25.252s. Root initially told main AutoMigrate incorrectly, immediately corrected after exact code read; actual fixture manually repeats115 alreadyloaded by commonsetup. Main owns minimal correction. Log Temp/weknora-root-t21-resource-docker-pg.txt; session36771 collected. No wholecoverage or fullT21passclaim.

Root actual quota log also shows existing OpenAI DEBUG batchinput length190 prints the entire short syntheticOther.java. This violates #29 no-whole-source logging when sourceindexing takes the existing path. Separate same-ticket privacy repair now assigned to accepted-parser originalT06/tree, owning only openai.go/zhipu.go/llm_debug.go plus NEW source_log_privacy_test.go, retaining actual provider input and metadata diagnostics. Main notified ownership; no broad model architecture or protocol change. This runtimefinding is additional to frozenbackend39eb's formal twoSpecP2, not a retroactive re-ranking of that static report. Root model/PG payloads are synthetic fixtures; no credentials or user production source exposed.

### Same-ticket root direction before freezing repairs

Main lease counter WIP initially incremented only direct expired Claim, but real scheduler RecoverSourceTriggers clears lease owner/expiry before re-dispatch. Root gave the exact scheduler path and durable-count/idempotent-poll requirement before freeze; migration118 up/down is necessary to persist the new column on existing production tables, not just add a GORM field. Main currently owns those migration files and real recovery regression.

Main read enrichment WIP initially required original snapshot.SyncLogID==currentlog.ID/GetRun(currentlog), incompatible with genuine unchanged-commit logs reusing an earlier published snapshot. Root identified the actual no-op path and requested exact stored-snapshot scope checks, no latest-snapshot substitution; unavailable/incomplete inventory must clear cached coverage into unknown. Main acknowledged and is implementing that edge coverage alongside requiredJSON/recovery fixes.

OriginalT06 privacy executor reports actual TDD reds for two HTTP providers' successful and oversized diagnostics and isolated LLM-debug output; root final native verification awaits cleanfreeze. Root owns no active Go/PG sessions; both independent root PG logs completed and collected. Root bounded parser test container stillrunning57823 for final combined acceptance, PGrootonly57822. No next-ticketdispatch/GitHubprocesscomments. Same-ticket scopes remain disjoint.

## Log privacy slice accepted: 98fe912c

Clean98fe912c82afcfcbd54f657761e0f22c4cfc7fa5 fromaccepted8b14, four owned files242+/20-. Worker actual TDD reds captured both providers' valid/invalid diagnostics and isolated LLM-debug input leak; worker reports wholeembedding packagePASS. Narrow Sol/high Standards0hard/2nonblockingjudgments (duplicate metadata branches and single-input test middleman), Spec0. No broad refactor requested.

Root independently ran TestEmbeddingProviderLogsKeepOnlyInputMetadata (four realHTTPsubcases) and TestEmbeddingDebugOutputOmitsInputText (isolatedchild): ALLPASS4.410s. Synthetic short/long sentinels absent from reviewed logs, metadata remains, actual mockrequest inputs exactlyunchanged. Session5067 collected. Slice merged into root; modelrequest/vector/retry paths untouched. WholeT21 remainsunaccepted pending main frozen telemetry/recovery fix and final combined gates; neither successful slice closes#29 or dispatchesT22.

## Frozen recovery repair and first root gates (2026-10-05)

Main froze fourteen owned files; its one Git add failed with permission denied, so root saved exact clean12f53a7038509df5397033bb50ebba74092bc72d. Native types three schema/allowlist cases PASS3.577s, but service compilation failed before PG assertions: duplicate addSourceTelemetryCounter declarations at source_telemetry.go33/74. This is a concrete compile blocker, not PG behavior failure. Both sessions20408/63424 collected; no root tests left running. Root returned the single-file correction to main, retaining existing overflow guard. Narrow Spec static0finding; Standards independently caught same compile issue outside documented/Fowler counts. No wholeT21 acceptance.

Same original helper2119 now owns only NEW source_resource_fault_review_integration_test.go for actual PostgreSQL53100 disk_full injection within isolated fixture schema, old-publication/ordinarydoc+B isolation and recovery. This deliberately does not claim a physically filled database volume. Existing quota91750/helper39d remain statically accepted and prior realPGgreen; root owns all PG/runtime tests. Parserprivacy98fe accepted/root089d, no nextticket or GitHub process updates. Main processedcursorc88d51d1-142b-47d5-8194-43f9bbc4b36b:99; parser839205de-410a-48f5-976e-ead813cc9283:70; helper18b45a08-19ec-408e-a3dd-033e67b23725:62. Root bounded parser57823 remains running for final gates.

Counter compile correction savedba53edd2; narrowbothaxes0newhard/0newjudgment/Spec0. Root assembled acceptedquota91750 and privacy98fe into main candidate1572881e. Actual native types/schema PASS4.000s and service counter PASS4.830s (session34050 collected). Tagged integration compilation still blocked by new test line169 using GetKnowledgeBaseByID on intentionally narrow interfacesKnowledgeBaseService. Root returned that precise test-only method correction to main; session17937 collected, no PG behavior claim. Root refuses to equate these compile attempts with acceptance.
