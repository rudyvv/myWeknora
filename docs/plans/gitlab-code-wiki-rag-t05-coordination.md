# T05 coordination note

T05 adds a narrow source-run status contract consumed by the existing sync-log UI:

- `internal/types/source_snapshot.go`: `detected_commit_sha`, `target_commit_sha`, `last_successful_published_at`, and `previous_published_at` are JSON-only status fields. They are not database columns and do not alter the immutable snapshot/publication schema.
- `internal/application/service/datasource_source_sync.go`: the current published snapshot is loaded before branch resolution so pre-scan failures can report the retained publication; a resolved branch HEAD populates detected and target SHA fields.
- `frontend/src/api/datasource/index.ts` and `frontend/src/components/SourceSnapshotRunView.vue`: the existing sync-log payload displays the four states without changing routes or publication/query scope.

No migration or shared source publication hunk is required. T05 does not modify T04's complete-manifest publication algorithm, T06 durable triggers, T20 webhook handling, or the global publication manifest.
