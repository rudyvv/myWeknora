# Source parser deployment and operations

The source parser is a separate, locked Python/Node service. The standard Docker deployment keeps its root filesystem read-only, drops all Linux capabilities, runs as UID/GID 65532, and connects it only to an internal Compose network shared with the app. It has no Git checkout, database credentials, persistent volume, or need for outbound network access. The app remains responsible for GitLab/model connectivity, source publication, storage accounting, and run telemetry.

## Standard Docker deployment

Build on a connected build host, then move the exact image to the air-gapped deployment host:

```powershell
docker compose -f docker-compose.yml -f docker-compose.source.yml build source-parser
docker image inspect --format '{{.Id}} {{.Config.User}}' weknora-source-parser:source-1.19.0-rules4-vue-sfc
docker save --output source-parser-image.tar weknora-source-parser:source-1.19.0-rules4-vue-sfc
```

Transfer `source-parser-image.tar` through the organization's approved artifact channel. On the target, import it and start the source overlay without pulling or rebuilding:

```powershell
docker load --input source-parser-image.tar
docker compose -f docker-compose.yml -f docker-compose.source.yml up -d --pull never
docker compose -f docker-compose.yml -f docker-compose.source.yml ps
```

The target must already have the matching app, database, and other Compose images. The parser image is built from a digest-pinned Python base, a hash-locked Python requirements file and grammar bundle, and the official Node archive plus Vue compiler locked by version and checksum. `npm ci` runs only in the image build. The parser does not download grammars, packages, or Node at startup. The runtime container is attached to an `internal: true` network; there is no published parser port. The app can reach `http://source-parser:8081` on that private network.

For Lite deployments, point the existing app setting `SOURCE_PARSER_URL` to the same parser service managed outside the application. Keep that endpoint on a trusted private network; the parser protocol is internal HTTP and has no caller authentication. If it crosses a trust boundary, terminate TLS at a private authenticated proxy and restrict access to the app. Do not expose port 8081 to users or the public Internet.

The Compose overlay defaults the parser to 768 MiB memory, 2 CPU cores, 64 PIDs, a 32 MiB `noexec` `/tmp`, read-only root, and an internal network. Use the finite parser controls below to tune accepted work; do not raise container limits without measuring peak working set and the host's capacity.

## Parser request and process limits

Limits are per parser process/container, not cluster-wide. Invalid, non-decimal, zero, or out-of-range values stop startup with a generic configuration error; the parser never silently disables a bound. The default maximum source file remains 16 MiB and is not configurable through these transport settings.

| Environment setting | Default | Allowed range | Bounds |
| --- | ---: | ---: | --- |
| `SOURCE_PARSER_MAX_CONNECTIONS` | 8 | 1–64 | Accepted HTTP connections and handler threads |
| `SOURCE_PARSER_MAX_REQUEST_BYTES` | 24,117,248 (23 MiB) | 1,024–33,554,432 | Complete JSON request body, including base64 expansion |
| `SOURCE_PARSER_HEADER_TIMEOUT_SECONDS` | 10 | 1–120 | HTTP request/header read time |
| `SOURCE_PARSER_BODY_TIMEOUT_SECONDS` | 10 | 1–120 | Absolute wall-clock time to receive the complete body; slow trickle does not reset it |
| `SOURCE_PARSER_PARSE_WORKERS` | 2 | 1–8 | Concurrent isolated parser child processes |
| `SOURCE_PARSER_PARSE_TIMEOUT_SECONDS` | 6 | 1–300 | End-to-end child deadline; timeout/cancellation kills and reaps the child process tree before capacity is returned |
| `SOURCE_PARSER_RESPONSE_TIMEOUT_SECONDS` | 10 | 1–120 | Response write deadline |
| `SOURCE_PARSER_MAX_RESPONSE_BYTES` | 33,554,432 (32 MiB) | 1,024–67,108,864 | Encoded parser result; larger results fail with a bounded error |

The internal `/health` response advertises parser readiness, verified languages, and configured limits. It is not an authenticated operational API and is reachable only on the private parser network. It does not claim live CPU/memory measurements or source-run telemetry; those belong to the app's source run view and deployment monitoring. Requests rejected for connection saturation return 503; worker saturation returns 429. Invalid input, oversize bodies, body deadline, parse deadline, and oversized output have bounded generic responses. Request bodies, source text, credentials, local paths, and parser exception strings are not logged or reflected.

The parser holds one bounded request body and a bounded number of worker processes in memory. Its grammar cache is baked into the immutable image. It does not use disk for Git clones, staging blobs, retained originals, or vectors. A full `/tmp` is therefore not a substitute for the app/database storage-exhaustion test; parser behavior under a full temporary filesystem is checked only to confirm the parser's memory-only request path remains usable.

## Source-run and storage policy in the assembled app

These are the approved application policy defaults for the complete T21 deployment. They are distinct from parser-container limits and from physical database free space. Source-run admission is one active source run per app process with a 30-minute deadline; document and Wiki worker lanes remain independently bounded, and model calls continue through the shared per-model governor. These process-local admission limits multiply with the number of app replicas unless the backend reports a distributed limit.

| Resource | Approved default | Meaning |
| --- | ---: | --- |
| Selected files per run | 10,000 | A complete scan must fail if this limit would truncate the manifest |
| Selected original bytes per run | 512 MiB | Sum of selected original payload, distinct from Git transport bytes |
| Git transfer / object staging | 1 GiB each | Finite transport and temporary object-store ceilings |
| Verified raw-blob spool | 512 MiB | Private bounded stage before publication |
| Per-file original bytes | 16 MiB | Parser/file ceiling; lower policy limits may apply |
| Temporary filesystem headroom | At least max(256 MiB, 20% of volume) | Physical free-space reserve before a run is admitted |
| Retained originals per source | 2 GiB logical | Includes pinned/history material and deduplicates shared immutable blobs |
| Parsed cache per source | 512 MiB logical | Separate from retained original bytes |
| Vectors per source | 4 GiB logical | Separate from raw and parsed payloads |

Logical payload quotas do not measure database page/index overhead or prove physical free space. Physical free-space checks and logical usage must be reported separately. Existing retention/GC ownership controls what can be reclaimed: pins and retained history are protected, and exhaustion must fail the candidate without truncating the complete scan or deleting evidence still in use. Check the actual database volume's free space with the database/storage operator; the app container's `df` output is not a reliable substitute for a remote database volume measurement.

The app's run details are authoritative for detected/target/published commit, stage counts and durations, parse quality, reuse, Wiki coverage, model calls/tokens, lease recovery, cleanup residue, and last successful publication. Missing measurements mean unknown, not zero. Actual provider token usage must not be replaced by estimates. Do not infer a published SHA from a failed run's target commit.

## Preflight: certificates, connectivity, and model limits

Run the preflight from the same app container/network and trust store that makes the requests. In Docker, use the script below after the overlay is healthy. It performs unauthenticated HTTPS probes with normal certificate validation, so it never needs a token; HTTP 401/403 means the endpoint was reached but does not verify credentials. Do not add `-k`/certificate bypasses. Install the organization's internal CA into the app runtime trust store through its approved image/secret procedure, then repeat the check.

```powershell
./scripts/source-operations-preflight.ps1 `
  -GitLabProbeUrl https://gitlab.example.internal/api/v4/version `
  -ModelProbeUrl https://models.example.internal/v1/models `
  -ModelIdentifier embedding-prod-2026-10 `
  -Tokenizer cl100k_base `
  -ConfiguredInputTokenLimit 8192 `
  -ProviderDocumentedHardLimit 8192 `
  -ProviderLimitReference 'example-provider-docs:model-card#max-input-tokens'
```

The reference above is a non-sensitive placeholder; replace it with the provider's public documentation URL or stable document identifier used to verify the hard limit.

Record the date, model deployment identifier, tokenizer/profile, configured input limit, provider-documented hard limit and its source, HTTP status/latency for GitLab and model endpoints, certificate-chain result, parser health and configured limits, app/database versions, and the database volume's physical free space. Record only hostnames and outcomes—never tokens, full credential-bearing URLs, request bodies, or certificate private keys. The configured limit must not exceed the provider's documented limit; verify model credentials and an actual representative call through the normal admin/model validation path, without pasting secrets into this script or its output.

For Lite, run equivalent TLS and reachability checks from the Lite app host/container to GitLab, the selected model endpoint, and the external parser endpoint. A successful host-browser check does not prove the application runtime trusts the internal CA or can route to those services.

## Temporary GitLab TLS troubleshooting exception

The app normally verifies GitLab's certificate chain and hostname. If an operator has explicitly approved a short diagnostic exception, the GitLab API client and the app's Go HTTP Git bridge accept this process-environment pair:

| Environment setting | Required value |
| --- | --- |
| `GITLAB_TLS_INSECURE_ORIGIN` | One exact `https://` origin only, with no path, wildcard, user info, query, or fragment; an explicit `:443` is equivalent to the default port. |
| `GITLAB_TLS_INSECURE_UNTIL` | RFC 3339 timestamp with timezone, strictly in the future and no more than 24 hours away. |

Both settings must be supplied together. If both are unset or empty, normal certificate verification remains enabled. An incomplete, malformed, or more-than-24-hours-ahead pair fails closed. A well-formed pair whose deadline has passed is inert: clients can still be created, but they use normal certificate verification. The exception is evaluated for every request and applies only to that HTTPS origin until the deadline; all other HTTPS origins and requests after expiry use independent normally verified connections. HTTP never matches the exception. Redirects from an active exception origin to another origin or to HTTP are not followed. Existing URL/SSRF checks, dial-time IP checks, timeouts, and transfer limits remain in force. This does not alter the parser, model clients, other connectors, or Git's configuration/environment, and it does not set `GIT_SSL_NO_VERIFY` or `http.sslVerify`.

This mode disables server certificate identity verification for the selected GitLab origin and therefore permits an active network attacker to impersonate that server during the window. It is a temporary connectivity diagnostic, not a successful TLS preflight and not a credential-validation result. Prefer installing the GitLab administrator's trusted CA in the app runtime or repairing the certificate chain. Do not enable this exception without explicit operational approval.

For an approved diagnostic, set the pair only in the app process environment (for example, in the deployment's protected `.env` file; do not put tokens or certificate material there), then recreate the app service so it reads the new environment. Use the exact origin from the configured GitLab endpoint, not a URL containing an API path or repository path. Record the chosen host and cutoff time without recording tokens or request contents. Do not extend the deadline beyond 24 hours.

To restore verification, remove both settings (or set both to empty), recreate the app service again, and rerun the ordinary preflight above from the app runtime. Confirm the certificate chain succeeds with the exception absent; only then treat TLS preflight as passed. Removing the variables from a running container is not sufficient—the app process must be recreated to receive the updated environment. Keep the temporary exception disabled until that separate operator confirmation.

## Isolated parser fault/offline verification

From the repository root, run:

```powershell
./scripts/source-parser-fault-offline.ps1
```

The script builds the current parser under a fresh per-run image tag, then runs the full parser HTTP suite and a fault fixture in a uniquely named, automatically removed container with `--network none`, read-only root, no added capabilities, `no-new-privileges`, 768 MiB memory, 2 CPUs, 64 PIDs, 32 MiB `noexec` `/tmp`, and a disposable 64 MiB executable test cache. Only the test directory is mounted read-only; parser/runtime code and locked assets come from the newly built image. No port, Docker socket, application service, database, or shared volume is attached. The fixture reports the effective cgroup/mount limits and checks offline startup/parse, real child crash and recovery, timeout/reaping, health and parsing while `/tmp` is full after ENOSPC, memory-pressure recovery, and process restart. Only the unique image tag created by this run is removed in cleanup.

The parser fault fixture does not prove source publication rollback, database/staging disk-full behavior, ordinary-document isolation, or provider-governor behavior. Those require the separately controlled app/PostgreSQL integration gates; do not describe this parser-only fixture as whole-pipeline acceptance or a performance benchmark.
