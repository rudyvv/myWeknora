# T12 f94b4a08 review and integration

Frozen clean worker SHA: `f94b4a085e90e8397ab7d13c1336412ce5b90248`. User-approved fixed base: `9e2ef8261d29f0d3fb74916de1d8e34dff58b594`. Two GPT-6 Sol/high agents independently reviewed Standards and Spec, reusing the completed unchanged c87d56d5 review and checking its repair delta. Integration uses a Conventional Commit squash.

## Standards

Documented violations: 0. Three non-blocking P3 judgement calls remain: repeated model-profile guards, separate Go/Python extension allowlists, and duplicated Go/Python text-fallback cuts that should retain equivalent contract coverage. No P3 was promoted into a documented-standard violation.

## Spec

Actionable findings: 0. The earlier P2 is closed: preview rejects deterministic full-path-header and minimum text-fallback body budget failures, marks their paths unindexable, and keeps CanSync false. It uses the existing Git blob scan and configured tokenizer without repository-wide parsing or embedding. Synchronization remains authoritative and preserves the prior publication on failure. Unsupported UTF-16 remains the approved explicit-failure boundary.

## Independent verification and integration

- Frozen branch: root reran both real PostgreSQL preview/header/body regressions; passed in 20.732s.
- Integrated Go source package passed (including the .styl routing case); modelcontext passed. The existing service preview fixture passed after supplying its now-required tokenizer/input-limit configuration. Frontend vue-tsc passed.
- Integrated real HTTP: template fallback, oversized comment/Unicode structure, XML/SQL lexical split and unsplittable token cases passed. Vue original-coordinate and sequential-request capacity cases passed after configuring the test subprocess module path; an initial missing PYTHONPATH was a test-launch error, not a product result. Standalone .styl HTTP admission retained exact UTF-8/CRLF bytes and text-fallback quality.
- Integrated PostgreSQL: minimum-body preflight, large Java/Mapper/fallback dual-index publication, staged-parse retry relationship retention, two Vue publication/representative cases, and pause fencing passed. The header case initially asserted the pre-T06 terminal status; its fixture now asserts the existing durable queued/retry_wait state and error, retaining all old-publication/file-version/content assertions. Root reran it successfully in 13.467s. Runtime recovery semantics were not weakened to satisfy the fixture.

Conflict resolution preserved T09 SFC region/quality/coordinate behavior and child-tree cleanup/pipe closure/slot release before HTTP response; it also preserved T06 embedding-version fencing, durable handoff and retry relation-member reuse. The representative repository's standalone Stylus extension is admitted as plain text, without executing preprocessors. A merge indentation error was fixed and all relevant Python files passed py_compile before HTTP verification.

T12 is accepted after these checks. Repository-wide scale/operational validation remains with the approved later tickets; current explicit preview/sync file and byte limits remain unchanged.

## Broader package checks and environment limits

After integration, root ran the complete tests of source, modelcontext, service, repository, types and datasource packages once, preserving JSON results in a local temporary log. Source, modelcontext, repository, types and every datasource connector package passed. The inherited `LOG_FORMAT=json` had first reduced log messages to the literal `json`: the existing logger treats this setting as a custom template, not an output-format enum. Clearing it only in the test process made the complete Feishu Wiki package pass (15.907s); product logging was not modified.

The complete service package did not pass on this Windows environment: unrelated sandbox tests rejected Docker's `npipe` scheme, shell tests required `sh` or executable Unix scripts, and existing skill-runtime/Python tests encountered unavailable local commands (exit 9009). These failing sandbox/skill files and logger/Feishu files have no changes between pre-T12 accepted integration `55847749` and `fa0dcad7`. This is evidence of unchanged code and concrete platform/command limits, not a claim that the entire baseline suite was rerun successfully. Source-specific service, parser, PostgreSQL and frontend acceptance checks above remain the evidence for T12. No broad suite is reported as fully green.
