# Source pipeline: Java, JavaScript, TypeScript, Python and Vue SFC (T02/T03/T07/T08/T09)

This worker accepts original UTF-8 content, SHA-256, a logical repository path and a byte budget. It receives no GitLab credentials or host file paths. Go fetches a fixed Git commit without checking out or executing the repository, verifies parser ranges against the original bytes, tokenizes the derived index text, stages both indexes on the built-in PostgreSQL database and atomically publishes the complete source manifest.

## Deployment

Use the normal PostgreSQL/ParadeDB deployment and the opt-in overlay:

```sh
docker compose -f docker-compose.yml -f docker-compose.source.yml up --build -d
```

Each parse runs in a request-owned process tree; timeout cleanup terminates and reaps only that tree before releasing parser capacity.

The app must be built from this branch and migrated through `000105`; T09 adds no database migration. The worker has no host port and shares an internal network only with the app. Runtime is non-root, read-only, with CPU/memory/process limits. Build acquires the pinned dependencies and verified language-pack 1.19.0 archive, then locks the Java, JavaScript, TypeScript, TSX and Python binaries. The SFC build separately verifies the official Node 24.19.0 archive against `sfc/runtime.lock.json` and installs the exact `@vue/compiler-sfc` 2.7.16 dependency tree with `npm ci`; runtime performs no downloads. Keep the grammar and SFC package files intact: a missing or changed locked runtime makes that capability unavailable. Health advertises the verified language set; each file returns the same parser version as worker health, incorporating the grammar set, Vue runtime and per-language extraction rules. Java, JavaScript, TypeScript and TSX remain on extraction rules v3; Python is rules v4; the locked Vue descriptor parser is rules v4. SFC parsing pins `NODE_ENV=development` so malformed-descriptor warning codes and ranges do not vary with the parent service environment. The Go source-processing version separately governs SFC block-kind classification, diagnostic evidence and index context, so changes to that contract invalidate cached parsed artifacts. A worker with Python reports `source-pack-1.19.0-rules-4-<fingerprint>`; the fingerprint also changes when Vue is available. Legacy four-language and Java-only grammar caches remain usable and do not advertise Vue unless JavaScript and TypeScript grammars plus the locked Node runtime are present. The app runtime includes Git; local app processes also need Git on PATH.

In knowledge-base data sources, choose GitLab source mode, one project and branch, and explicit source paths. Preview must show all readiness checks passing, including the actual grammar/runtime for every selected language. Initial sync requires 1–100 selected Java/JavaScript/TypeScript/Python/Vue files; an existing publication may become empty after a complete update. The bounded pipeline allows at most 100 selected files and at most 16 MiB in total, UTF-8/BOM only, on the built-in PostgreSQL store with keyword and vector indexing enabled and an active embedding model. Selected unreadable members fail the run; explicitly excluded members remain in its inventory. These languages may coexist in one snapshot. Java-only legacy grammar caches remain valid for Java paths and explicitly refuse languages absent from their lock.

Start manual sync, open its run details, and follow fetching → parsing → indexing → published. The manifest includes excluded members, stable file identities and immutable file-version identities. Published members open a read-only code view with symbols, line numbers and a same-SHA GitLab link. Search a known method through either keyword or vector retrieval. Tags, description and custom metadata stay manageable; ordinary body/chunk edits, enable toggles, reparse, delete and cross-KB transfer are rejected.

`GET /api/v1/knowledge/:id/source?version_id=...` reads a published file after existing KB authorization and verifies its stored checksum. An explicit version must match current publication; it never silently substitutes another version. Retained source evidence is readable only through its authorized Wiki page/revision owner, as described below; this general endpoint does not grant arbitrary historical versions. Raw download preserves the original bytes. Chunk `chunk_metadata.source` carries original byte/line ranges, separate contexts, symbols, quality, snapshot/version and GitLab URL. Vue chunk evidence additionally identifies its template/script/style/custom region; wrapper tags and gaps remain separate exact source chunks, not a fabricated combined span. External `src` values stay literal and unresolved in stored evidence, where `unchecked` means no authorization-scoped read has happened yet. A file read resolves one only when the target is selected in the same snapshot and is also allowed by the caller's current combined file/tag scope; absent and out-of-scope targets both return the generic `unavailable` status without a resolved path.

The current index-text ceiling is 2,000 tokens using the existing `cl100k_base` tokenizer. This is an approximation for other model families, not a claim to match every remote model tokenizer. The mature chunker budgets bytes; Go checks the complete derived text and reduces that budget when needed. Oversized context that cannot fit is rejected. Parser HTTP is a bounded first slice; large-file streaming and broader language handling are later work.

## Scoped questions (T03)

HybridSearch and knowledge-QA requests accept `source_ids` alongside their existing KB, file and tag selections. These selectors intersect. A question pins each contributing source's published snapshot before retrieval or model work; code reads, grep, chunk pages, enrichment and citations use those same members. Publishing a newer snapshot does not switch an in-progress question. Current permission revocation and explicit source clearing stop further reads and output. The durable lease expires within 30 minutes or at an earlier request deadline, and normally releases when the question/stream finishes. Registered Wiki evidence can retain historical code references as described below; garbage collection remains a later ticket.

Migration `000103` is required for this behavior. Source-scoped vectors rank the complete relationally eligible candidate set before topK, so narrow file/tag scopes cannot disappear behind an ANN candidate budget. Representative-repository latency is still to be measured. See [T03 validation](../docs/plans/gitlab-code-wiki-rag-t03-progress.md) for real-index coverage and Windows full-suite limitations.

## First module Wiki card (T14)

Enable Wiki and configure its existing synthesis/summary model, then use the source-module panel in the Wiki browser to choose one source and a small module directory. This slice creates one module responsibility card under a source-specific identity; it does not automatically generate a page per file. The collector accepts at most 16 non-generated files and 32 KiB of complete raw evidence. Larger modules are rejected with a reason so an administrator can choose a smaller directory. Generation and independent semantic QA share a three-minute model deadline, at most 18 charged provider calls, a conservative 360,000-token budget and at most two repairs. Failed attempts keep their reason/counters and do not replace a ready card or delay source publication; the UI offers a new bounded manual attempt.

Models return only supplied evidence IDs. The server validates immutable SHA, checksums and original ranges, saves the ready body together with its provenance and registered raw owners, and exposes references in the existing read-only source viewer. Every model-visible file is conservatively registered, because titles, summaries and QA can use evidence beyond a section's explicit citation. Technical pages and their body, summary, related-summary, directory and history projections require all actual contributions to satisfy the question's original repository/file/tag alternatives. Ordinary document Wiki retains its existing any-source behavior. Unverified or stale technical cards are excluded from current answers; authorized management/history can still read their exact body and retained evidence.

`GET /api/v1/knowledgebase/:kb_id/wiki/source/evidence?slug=...&version=...&evidence_id=...` resolves an exact registered page or revision owner after fresh authorization, rather than granting access by an arbitrary file-version handle. A retained owner may refer to a file removed from the current publication, including a complete empty update, while its current source publication still exists. Permission revocation and explicit source clearing override this retention. Manual edits invalidate technical readiness; rollback retains the selected body's own provenance and recomputes applicability. Migration `000105` is required. These first-card limits are deliberate; automatic coverage, affected-card regeneration, cumulative budgets and raw-evidence garbage collection remain later tickets.

## Reproducible checks

JS/TS route `.js`, `.jsx`, `.mjs`, `.cjs` to JavaScript, `.ts`, `.mts`, `.cts` to TypeScript, and `.tsx` to the separate TSX grammar. Python routes `.py` to the separately locked Python grammar. Vue `.vue` files use Vue 2.7.16 `@vue/compiler-sfc` only to identify block boundaries; templates, styles and custom blocks are not compiled. Inline script bodies use the corresponding locked JavaScript/TypeScript/TSX Tree-sitter grammar. External scripts are never loaded or executed; only a public source-file read may mark a literal relative `src` resolved, and only when the target is a parsed member in that same snapshot and the current caller scope permits reading it. Unknown script dialects and preprocessing remain byte-exact raw chunks with `unknown_preprocess` quality; parse warnings and external-only scripts are `degraded`. SFC compiler warnings appear as generic diagnostic symbols without echoing parser messages or source text. The compiler's UTF-16 offsets are mapped to original UTF-8 bytes and verified; CRLF, BOM, Chinese and emoji remain unchanged. Every wrapper, body and gap chunk covers an adjacent original range, so no chunk invents a contiguous span across disjoint SFC areas.

Opening and closing tags for empty external scripts and empty unknown-preprocessor blocks are retained as positive-width original-byte chunks with their region and degraded/unknown-preprocess quality. Model-facing source metadata exposes only the bounded `unchecked` or `rejected` external status; it omits the literal external reference and any resolved repository path.

Extraction rules v3 preserve module, static ES imports and TS import-equals, export/default-arrow, function/arrow, class/method, namespace, interface/type/enum and parent structure. Python extraction rules v4 preserve module, import/import-from statements, class, function/method, async signatures and decorators, using iterative syntax-tree traversal. Imports and decorators are syntactic records; imported modules and decorator/framework behavior are never executed or inferred. Multiline strings stay in exact source chunks and do not create declaration or import symbols. Named object bindings preserve their syntactic parent, including nested objects. Destructuring RHS objects do not receive a guessed binding identity; full text remains readable under the existing structural contract. This is syntax/structure extraction, not runtime dependency or type resolution. Syntax errors mark the relevant script region; the worker must retain every original byte or refuse the result.

`tests/fixtures/reservations.ts` is an independent, manually checked TypeScript corpus, rather than evidence from the Java/Vue representative repository. Its CRLF, Chinese identifier, generic namespace/class/method and decorators have literal coordinate expectations; fixture-local Git attributes preserve those bytes across checkouts. The service integration test also uses it in actual fixed-commit sync, both PG indexes, public tools and original-file reads. See [T07 validation](../docs/plans/gitlab-code-wiki-rag-t07-validation.md).

Build and exercise the actual parser without network access:

```sh
docker build -t weknora-source-parser:t09-rules4-vue-sfc sourceparser
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --memory 768m --cpus 2 --pids-limit 64 \
  --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  --tmpfs /contract-cache:rw,exec,nosuid,size=64m -e TMPDIR=/contract-cache \
  --mount type=bind,source="$PWD/sourceparser/tests",target=/opt/source-parser/tests,readonly \
  weknora-source-parser:t09-rules4-vue-sfc python -m unittest discover -s /opt/source-parser/tests
```

The tests copy verified grammar bytes to a temporary legacy cache. For Python coverage, use a separately prefetched five-language cache; do not add Python binaries to the previously accepted four-language cache. Only an isolated contract cache permits loading its trusted parser library; the deployment keeps `/tmp` mounted with `noexec`. Repository code is never executed.

For the service integration suite, use a dedicated database (never a deployment DB), real local Git and a prefetched Python 3.12 runtime:

```sh
docker run -d --name weknora-code-source-t02-test \
  -p 127.0.0.1:57520:5432 -e POSTGRES_USER=source_test \
  -e POSTGRES_PASSWORD=source_test -e POSTGRES_DB=source_test \
  paradedb/paradedb:v0.22.2-pg17
python3.12 -m venv .source-parser-venv
.source-parser-venv/bin/pip install --require-hashes --only-binary=:all: -r sourceparser/requirements.lock
.source-parser-venv/bin/python sourceparser/prefetch.py --cache .source-parser-grammar
export SOURCE_TEST_POSTGRES_DSN='postgres://source_test:source_test@127.0.0.1:57520/source_test?sslmode=disable'
export SOURCE_TEST_PYTHON="$PWD/.source-parser-venv/bin/python"
export SOURCE_PARSER_CACHE="$PWD/.source-parser-grammar"
go test -tags integration ./internal/application/service -run '^TestSource' -count=1
```

Fixtures require loopback and database name `source_test`, use independent temporary schemas and assert through public services/Agent tools. Only GitLab transport, embedding responses and task queue are controlled external boundaries. Real parsing and both actual indexes remain in the test. Failures include staging reads, missing keyword index, zero vector and unreadable selected source. The tests do not measure representative-repository relevance or performance; those belong to the final acceptance ticket.
