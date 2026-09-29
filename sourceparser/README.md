# Initial Java source pipeline (T02)

This worker accepts original UTF-8 content, SHA-256, a logical repository path and a byte budget. It receives no GitLab credentials or host file paths. Go fetches a fixed Git commit without checking out or executing the repository, verifies parser ranges against the original bytes, tokenizes the derived index text, stages both indexes on the built-in PostgreSQL database and atomically publishes the complete source manifest.

## Deployment

Use the normal PostgreSQL/ParadeDB deployment and the opt-in overlay:

```sh
docker compose -f docker-compose.yml -f docker-compose.source.yml up --build -d
```

The app must be built from this branch and migrated through `000103`. The worker has no host port and shares an internal network only with the app. Runtime is non-root, read-only, with CPU/memory/process limits. Build acquires the pinned dependencies and grammar archive; runtime performs no downloads. Keep the grammar image intact: missing or changed grammar makes health and parsing unavailable. The app runtime now includes Git; local app processes also need Git on PATH.

In knowledge-base data sources, choose GitLab source mode, one project and branch, and explicit Java paths. Preview must show all readiness checks passing. T02 allows 1–100 selected Java files and at most 16 MiB in total, UTF-8/BOM only, on the built-in PostgreSQL store with keyword and vector indexing enabled and an active embedding model. Selected unreadable members fail the run; explicitly excluded members remain in its inventory. Larger/mixed-language repositories and distributed index stores are handled by subsequent tickets.

Start manual sync, open its run details, and follow fetching → parsing → indexing → published. The manifest includes excluded members, stable file identities and immutable file-version identities. Published members open a read-only code view with symbols, line numbers and a same-SHA GitLab link. Search a known method through either keyword or vector retrieval. Tags, description and custom metadata stay manageable; ordinary body/chunk edits, enable toggles, reparse, delete and cross-KB transfer are rejected.

`GET /api/v1/knowledge/:id/source?version_id=...` reads a published file after existing KB authorization and verifies its stored checksum. An explicit version must match current publication; it never silently substitutes another version. Retained historical-reference reading comes in later tickets. Raw download preserves the original bytes. Chunk `chunk_metadata.source` carries original byte/line ranges, separate contexts, symbols, quality, snapshot/version and GitLab URL. It does not describe the derived index header as contiguous source.

The current index-text ceiling is 2,000 tokens using the existing `cl100k_base` tokenizer. This is an approximation for other model families, not a claim to match every remote model tokenizer. The mature chunker budgets bytes; Go checks the complete derived text and reduces that budget when needed. Oversized context that cannot fit is rejected. Parser HTTP is a bounded first slice; large-file streaming and broader language handling are later work.

## Reproducible checks

## Scoped questions (T03)

HybridSearch and knowledge-QA requests accept `source_ids` alongside their existing KB, file and tag selections. These selectors intersect. A question pins each contributing source's published snapshot before retrieval or model work; code reads, grep, chunk pages, enrichment and citations use those same members. Publishing a newer snapshot does not switch an in-progress question. Current permission revocation and explicit source clearing stop further reads and output. The durable lease expires within 30 minutes or at an earlier request deadline, and normally releases when the question/stream finishes. Historical code references and garbage collection remain separate later tickets.

Migration `000103` is required for this behavior. Source-scoped vectors rank the complete relationally eligible candidate set before topK, so narrow file/tag scopes cannot disappear behind an ANN candidate budget. Representative-repository latency is still to be measured. See [T03 validation](../docs/plans/gitlab-code-wiki-rag-t03-progress.md) for real-index coverage and Windows full-suite limitations.

## Reproducible checks

Build and exercise the actual parser without network access:

```sh
docker build -t weknora-source-parser:t02 sourceparser
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges --memory 768m --cpus 2 --pids-limit 64 \
  --tmpfs /tmp:rw,noexec,nosuid,size=32m \
  --mount type=bind,source="$PWD/sourceparser/tests",target=/opt/source-parser/tests,readonly \
  weknora-source-parser:t02 python -m unittest discover -s /opt/source-parser/tests
```

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
