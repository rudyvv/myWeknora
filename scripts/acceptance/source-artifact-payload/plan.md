# T22 artifact-payload helper — frozen source, no DB mode executed

The original plan and helper were ignored under `.cache/`. This byte-preserving copy of the Go source is now tracked under `scripts/acceptance/source-artifact-payload/`. Helper source
and pure tests are frozen here for Root review. The original helper author did not execute a live mode or database
connection, backend, or service was run. Pure tests use a dummy DSN with an
injected connector.

## Fixed execution contract

- Modes: default stat-only (no environment read and no DB open); `--preflight` (fixed clone, read-only transaction and guarded migration settings); `--upgrade-only`
  (fixed migration 120 clean → 121); `--backfill` (requires 121); `--measure`
  (read-only, requires 121 and zero scoped NULL cache counts).
- Target clone: `source_t22_live_rehearsal_20261006`; admin DSN is supplied by
  Root through `SOURCE_TEST_POSTGRES_DSN`. Parse it with pgx and accept only
  host `127.0.0.1`, port `57822`, database `source_test`, `sslmode=disable`.
  Change only the database to the fixed clone, pin `search_path=public`, remove
  TLS fallback, and set `ValidateConnect` to verify the actual clone/schema.
  Never load dotenv or print the DSN/credentials.
- Migration 121 SQL must match SHA-256
  `2D09D4CAEEA5B2528661CBCB282A3CF7BFFC059225995C5387FD2D14B9D86540`.
  `--upgrade-only` accepts only ledger version 120, clean; use the normal
  golang-migrate PostgreSQL advisory lock and one transaction for the exact SQL
  plus ledger update to 121, rolling back both on any error. No migration
  command is executed by this plan.
- Tenant is `10000`; KB is exactly
  `65658207-a2ec-47fb-bf0f-11e7b685369e`; target schema is `public`.
- Backfill cap: 10,000 rows total, 64 rows maximum per transaction, 180 seconds
  total. Stop at either total cap and report partial/resumable; only zero
  remaining scoped NULL rows is complete. Each transaction locks the three
  source-state rows in sorted order and rejects missing state or non-NULL
  active/pending/lease fields. Root pauses sources; backfill and measurement
  require a caller assertion that the application is stopped. Never forge a
  source lease/context.
- Every write mode requires the explicit
  `--stop-proof=process-and-clone-sessions` flag; missing or different values
  are rejected during argument parsing, before DSN access or connection.
  Immediately before migration DDL/DML and before every backfill batch, freshly
  hash the pinned backend executable at
  `C:\Users\28211\.codex\test-runners\weknora-t22-capacity-2430a0e8.exe`
  against SHA-256
  `a7c29a071b552b6008d8b6e827853aac56114fe4487204b2e6564ce0269ce803`, then
  enumerate Windows processes and compare each same-name candidate's full
  executable path. The exact-path process count must be zero. Missing file,
  hash mismatch, incomplete enumeration, or unreadable candidate path fails
  closed; process name alone is never treated as identity. Non-Windows builds
  use a fallback enumerator that returns unsupported, so write proof remains
  fail-closed there.
- On the already-pinned `source_t22_live_rehearsal_20261006/public` connection,
  each write proof counts all rows in `pg_stat_activity` for the current
  database except the helper's own backend PID, regardless of session state
  (including `idle`), and counts `pg_prepared_xacts` for that database. Both
  counts must be zero. Identity mismatch, permission/query/timeout errors, or
  any nonzero count fails closed. Before the write callback, stderr receives
  only the three numeric proof counts (target processes, other clone sessions,
  prepared transactions); if reporting fails, no DDL/DML callback runs.
- Upgrade and backfill acquire the same helper session advisory lock to prevent
  helper/helper concurrency. This lock does not constrain external launchers or
  clients. A real write run additionally requires a Root-owned maintenance
  window: no backend start/restart and no new clone clients from upgrade
  through every batch and cleanup/connection close. Do not stop, restart, or
  otherwise touch the application on port 8080.
- Root will launch real helper execution under a 180-second external process
  watchdog, which is the hard 180-second wall-clock limit; do not extend it to
  188 seconds. Go context deadlines do not preempt synchronous executable-file
  reads/hashing or a blocking `io.Writer.Write`, so the external watchdog may
  terminate the helper before its internal 8-second cleanup reserve begins.
  If it fires, treat the result as unknown/partial, do not rerun, and first
  perform read-only state verification and get Root review because a
  transaction or commit outcome may be uncertain. The internal 8-second
  reserve is best-effort cleanup during normal helper execution, not a promise
  that cleanup completes before the hard watchdog. Neither deadline relaxes
  the fresh process/session proof before migration DDL/DML or every backfill
  batch.
- Backfill updates NULL metadata only, using bounded SQL/CTE row selection:
  parsed `octet_length(parsed::text)`, vector
  `jsonb_array_length(vector)::bigint*4`. Never fetch artifact JSON into Go or
  modify payloads, identities, counters, chunks, embeddings, quotas,
  publications, or auth state. Recheck clone/version/scope/quiescence per batch.
- Progress and measurement output is numeric metadata only; no IDs, content,
  artifact keys, path, DSN, or credentials.
- Measurement uses three paired old/new runs. Each pair executes the exact old
  and production-new full quota aggregates for all three sources in the same
  `REPEATABLE READ READ ONLY` transaction; alternate query order. Require
  per-source equality of original, parsed-cache, and vector bytes. Report only
  numeric durations/totals/equality/medians. This is parity plus a bounded
  timing observation, not a whole-T22 or production performance claim. It does
  not probe a loopback service port; preserve the fixed clone/version, paused
  source, global-quiescence, zero-scoped-NULL, and read-only transaction guards.

## Corrected fixed source scope

Root corrected the accidental labels; the frozen helper now uses exactly these
persisted UUIDs, each 36 characters:

- `ba6d2417-222e-4ff9-be22-eccf28659b32`
- `6168f382-c25d-4914-95c3-9bbecca7f18d`
- `ab31aae4-b21f-4dc4-90d6-d7e506c29c66`

The helper uses the golang-migrate lock key for URL path `/<clone database>`
(expected numeric key `902578274`), locks the exact ledger/source/state rows,
and fails closed on global schedule, sync, Wiki, task, cleanup, or outbox work.
No helper mode probes port 57825. The old loopback refusal/timeout classification
tests are removed because those outcomes are no longer part of the execution
contract. The actual `run` dispatch path is tested through an injected connector
for upgrade, backfill, and read-only measurement, without opening a socket.

The pure helper suite passes with
`go test -count=1 ./scripts/acceptance/source-artifact-payload`.
Native Windows `go build` and Linux/amd64 cross-build
(`GOOS=linux GOARCH=amd64 go build`) both exit 0. Both emit a non-fatal Go
module stat-cache write `Access is denied` warning; generated executables were
removed and no cache path was changed. Tests cover mandatory write-proof
parsing, actual run dispatch without port preflight, pinned executable SHA and
exact-path process checks, clone-session and prepared-transaction rejection,
safe numeric reporting before writes, mid-run process/session changes stopping
later batches, and write-callback ordering. Existing deadline,
rollback/commit uncertainty, row-cap, resumability, fixed-scope, and migration
tests remain. Measurement implementation was not changed: it retains its
read-only transaction and fixed clone/source/global guards. No clone state,
source payload, auth/publication state, or production system was read or changed
here.

## Frozen review manifest

The complete review set is every Go file in this directory, this plan, and the
pinned migration SQL used by `--upgrade-only`. Source and migration hashes are:

- `scripts/acceptance/source-artifact-payload/main.go` — `919F7D51FA2DC755E913D9C0A2B13E34BD899F7A6BBBA1E302A8F86C3ED3E9E2`
- `scripts/acceptance/source-artifact-payload/main_test.go` — `44F4366861B57B88CDD89254B076BFC61165D78378B483636BBE107909FDFF67`
- `scripts/acceptance/source-artifact-payload/process_guard_windows.go` — `8E52D749BED4970D8468DE8E7B7E282B0D0FD58FE143546991C5D1983374BB96`
- `scripts/acceptance/source-artifact-payload/process_guard_other.go` — `92850C0A979599798F8FDA7A60FA564D35E6F7AC50CA45212FB05A5784EC7052`
- `scripts/acceptance/source-artifact-payload/stop_proof_test.go` — `4B8C27F2299AD3B5459799AC2674F0416FD3B5762939D775EE90C68CE13DF376`
- `migrations/versioned/000121_source_artifact_logical_payload_bytes.up.sql` — `2D09D4CAEEA5B2528661CBCB282A3CF7BFFC059225995C5387FD2D14B9D86540`

The SHA-256 of this `plan.md` is recorded in the external review handoff after
the final document edit, avoiding a self-referential hash.

## 2026-10-07 Root freeze

The initial copied Go hashes matched the handoff; main.go is now revised as described below. The original plan raw SHA256 is 9E82E3185E8196698063DA8FD64BB61A6284E8098375EA065AD30E6730512510, differing from the handoff A67E92CB863637C2FD1A90F3ADDF11140455CB8FCA2B28EBD73134C295E0695F; both independent reviewers reviewed actual bytes. Migration 121 must be checked out with LF: its approved Git blob hash is unchanged. A narrow .gitattributes rule pins its working-tree bytes to the required 2D09 hash. Root read-only inspection found version120/clean, fixed scope and paused/quiescent state, zero other clone sessions/prepared transactions/pinned processes. No migration, backfill or performance measurement has run yet. The separate ignored readonly inspection adapter is not part of this write helper.

## Real PostgreSQL transaction preflight repair

The first reviewed upgrade exited2 with migration_upgrade_rejected before the write proof callback. Immediate read-only inspection still showed120/clean and zero sessions/tasks. A new public --preflight mode reproduced SQLSTATE42601 in a read-only transaction: PostgreSQL does not accept the former combined SET LOCAL statement. Migration and preflight now share separate valid statement_timeout=8s and lock_timeout=3s statements. The tagged integration CLI test was red with42601 and then green after the fix. It only uses the existing dedicated clone read-only; no test migration writes are allowed. All prior write proofs, locks, fixed scope, limits and quota formula remain. main.go hash in the manifest above now binds the corrected version; preflight_integration_test.go SHA256 D492C4C322659D8F05DD4B8BBC4FD5CC0F37DDA38B55B4317F18FFDAA54B794F. Root must review the new frozen commit before any write retry. No upgrade, backfill or quota performance measurement has completed yet.
