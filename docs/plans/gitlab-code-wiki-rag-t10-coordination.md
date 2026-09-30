# T10 coordination: static MyBatis source relations

This note records the implemented T10 contract and the validation actually
performed. It is subordinate to the parent T10 plan/spec and does not claim
that unrun acceptance tests passed.

## Implemented contract

- Java and MyBatis XML facts are produced by the deployed Python HTTP parser
  (`sourceparser/runtime.py`, `sourceparser/mybatis_parser.py`). XML structure
  uses Expat with external entities disabled; SQL table extraction uses
  SQLGlot's MySQL AST. Source facts carry ranges into the original bytes.
- Go `source.CorrelateSourceFacts` consumes only allow-listed parser facts
  belonging to one staged snapshot. It creates interface-method-to-XML-statement
  relations and owner-bound MyBatis include/resultMap-reference relations only
  when namespace, owner, and target identities are unique and acyclic. It does
  not join Java call sites, fields, receivers, or inferred scopes.
  `java_field` and `java_mapper_call` are not emitted or accepted by the Go
  parser client. Overloaded or unresolved mappings remain uncertain with a
  reason; table accesses do not invent a readable source-file target.
- `SourceCodeRelation` rows are staged with the snapshot and published through
  the existing publication transaction. A failed relation stage cannot publish
  partial edges; replacing the publication with an empty complete manifest
  clears the active relation set without rewriting pinned historical snapshots.
- `GET /api/v1/knowledge/:id/source` continues to use its existing access
  validation and does not widen its default read scope. Relation pages are
  bounded to 100 rows (`limit + 1` detection) and expose
  `relations_truncated` plus an opaque keyset cursor bound to the selected
  snapshot, file, and version. Every page revalidates the read lease and both
  relation endpoints against the same tenant/KB/snapshot/file/tag scope.
- Agent `list_knowledge_chunks` reads source analysis only when an existing
  question `SourceRead` scope is present. It requests the exact version pinned
  in chunk evidence, returns bounded fact/diagnostic/relation summaries, and
  fetches each verified cross-file endpoint through the existing
  `GetSourceFile(ctx, id, version)` API under that same scope. Relation cursors
  are main-file-bound and are cleared only for cross-file endpoint reads; the
  source lease, endpoint version, and snapshot pin remain unchanged. Target
  evidence reports the actual file/version/SHA/range and a byte-sliced snippet;
  it does not manufacture a source span for SQL text.
- The shared `modelcontext.Registry` formatter reads `source_analysis` from
  structured `ToolResult.Data`, emits a bounded JSON sidecar inside its
  retrieval output, and XML-escapes source text. It does not rely on the
  UI-oriented `ToolResult.Output` append surviving model reformatting.
- `SourceCodeView` and the Agent knowledge-chunks result display known quality
  labels, typed facts, parser diagnostics, relation certainty/reasons, bounded
  relation continuation, and any target evidence returned by the scoped Agent
  read. Unknown quality values are labeled as unknown rather than as syntax
  errors.

## Parser regression coverage

The SQLGlot HTTP contract covers INSERT/UPDATE/DELETE, joined DML, derived
subqueries, nested and qualified CTE visibility, table-valued functions, and
DELETE target aliases. In particular,
`UPDATE orders o JOIN (SELECT * FROM customers) c ...` reports both `orders`
and `customers`; CTE references are excluded while physical tables inside CTE
bodies remain included. The parser rules fingerprint is `rules-10`, reflecting
the removal of unsupported Java field/call facts and the added owner-bound
MyBatis fragment/result-map reference facts.

## Read-only representative validation

The production parser HTTP client and Go correlator were run over only the five
authorized representative files. The temporary validator printed aggregates
only; no source or SQL was saved in this repository.

| Representative file | Bytes | Quality | Structural facts / diagnostics | Fact line bounds |
| --- | ---: | --- | --- | --- |
| `BusinessPushScheduleMapper.java` | 1,246 | structural | 9 mapper methods; no diagnostics | methods 14–37 |
| `BusinessPushScheduleDetailedMapper.java` | 2,620 | structural | 16 mapper methods; no diagnostics | methods 14–72 |
| `BusinessPushScheduleMapper.xml` | 14,115 | structural | 9 statements, 14 table facts, 6 includes, 1 resultMap, 3 fragments; 6 SQL-parse uncertainty and 2 dynamic-fragment diagnostics | mapper 3–315; statements 81–312; table facts 81–312 |
| `BusinessPushScheduleDetailedMapper.xml` | 27,102 | structural | 16 statements, 31 table facts, 7 includes, 1 resultMap, 3 fragments; 8 SQL-parse uncertainty and 2 dynamic-fragment diagnostics | mapper 3–594; statements 81–592; table facts 81–415 |
| `BusinessFreeTutorMapper.xml` | 63,154 | structural | 34 statements, 150 table facts, 7 includes, 1 resultMap, 3 fragments; 8 SQL-parse uncertainty and 2 dynamic-fragment diagnostics | mapper 3–1,274; statements 87–1,272; table facts 87–1,224 |

Across those facts the correlator produced 245 relations: 25 certain mapper
method-to-statement relations, 20 certain include relations, 5 certain
resultMap relations, and 28 certain table accesses. The remaining 167 table
accesses were uncertain with reason “SQL table access is branch-dependent or
contains a dynamic identifier.” The SQL-parse diagnostics remain a known
limitation; no representative SQL text was copied here.

## Validation status and remaining limits

- Python parser HTTP contracts: 33 tests passed, including derived-DML and
  exact-range fragment/result-map reference regressions.
- Go `internal/source`, `internal/application/repository`, and
  `internal/modelcontext` suites passed. Focused Agent source-analysis tests
  and the HTTP handler cursor/version handoff test passed.
- The tagged PostgreSQL test
  `TestSourceMyBatisMapperXMLFactsRelationsScopesAndIndexes` passed against the
  designated isolated loopback database. Its real parser/client/correlator
  fixture verifies fragment-to-fragment include and resultMap extends/nested
  relations with exact source ranges, plus a 110-edge continuation whose next
  page reads cross-file target evidence without reusing the main-file cursor.
  No database credentials were read from another container or conversation.
- The focused `internal/handler` route test passed: `relation_cursor` and
  `version_id` reach the scoped source read, and the response remains
  `private, no-store`.
- The broad Windows Go package run had unrelated environment failures involving
  `/bin/bash`, symlink privileges, file-URI schema validation, and frontend
  capability parsing. The worker did not run frontend component tests; the
  `KnowledgeChunksList` test mount now supplies its required `$t` global, and
  root is running the approved read-only frontend verification separately.

These results cover parser, source paging, scoped Agent evidence, and static
correlator behavior, not every T10 acceptance criterion. Frontend component
tests and type-check remain to be recorded from root's isolated dependency
environment.

## Integration handoff

Keep the T10 facts/diagnostics/relations UI and API type changes in this
freeze. `SourceCodeView.vue` currently contains a temporary local
`sourceQualityLabels` mapping; T09 owns the shared complete quality helper and
Region/modelcontext integration. During root integration, retain T09's full
quality mapping rather than replacing it with this interim mapping, while
preserving the independent T10 source-analysis UI.
