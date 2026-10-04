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
