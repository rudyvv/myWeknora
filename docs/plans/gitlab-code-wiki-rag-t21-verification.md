# T21 verification ledger

T21/#29 is in progress, not accepted. Approved review base is `c0747374a96908b98c29aaa49526d7dbd0ec48f9`. Contract and slice ownership: gitlab-code-wiki-rag-t21-coordination.md. Root is responsible for independent dual review and combined acceptance; no slice's partial green result closes the ticket.

## Model cancellation boundary (host, 2026-10-05)

Original T09/a8ea stable four-file work-in-progress window, before backend resource implementation. Worker Go build-cache access denial occurred before tests and is not behavioral red evidence. Root reviewed the test's first-provider counter reset and bounded waits, then ran actual native model-package tests without PostgreSQL.

`go test ./internal/models/chat ./internal/models/embedding -run 'TestConcurrency.*' -count=1 -json`: eight actual tests PASS; chat package 4.342s and embedding package 4.731s. Both new canceled-waiter provider counters PASS, with prior interactive-bypass, stream-release, background-limit, per-model-limit and pool fan-out regressions still green.

Root then overlaid only the two original `c0747374` production wrapper files through Go's temporary overlay, leaving the worker's files untouched. The same new tests actually failed: chat started a provider call after canceled semaphore waiting (package 4.185s); embedding canceled waiting did not return (case 2.01s, package 6.155s). This establishes an actual before/after behavioral regression, not an implementation-mirroring or compilation-only check.

Local bounded logs: Temp/weknora-root-t21-model-cancel.jsonl and Temp/weknora-root-t21-model-cancel-baseline.jsonl. Sessions 56201 and 7673 fully collected. Worker four-file mutation window released. No shared dependency restart, old container credential reads, or PG activity. Whole-ticket clean frozen review and resource/offline/fault/end-to-end gates remain pending.
