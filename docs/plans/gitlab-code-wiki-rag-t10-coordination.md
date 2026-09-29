# T10 coordination: static code relation storage

T10 adds the smallest additive relation contract needed by later business-flow
and retrieval work. It does not change the source publication transaction,
parser HTTP route, source permissions, or Wiki evidence rules.

## Contract

- `internal/source/mybatis` parses one fixed XML/Java input set and returns
  exact original byte/line ranges plus `certain` or `uncertain` determinacy.
- `SourceCodeRelation` stores one immutable edge under
  `(tenant_id, data_source_id, snapshot_id)`. `from_file_id` and
  `from_version_id` identify the immutable source version; table accesses may
  leave the target file IDs empty and use `to_key` for the table name.
- `000107_source_code_relations` is additive and does not alter `000104` or
  `000105`. `ReplaceSnapshotRelations` is the only writer contract; readers
  must supply the fixed snapshot ID and tenant/source scope.
- Missing/duplicate namespace, statement, include, or resultMap targets remain
  `uncertain`; dynamic SQL table candidates remain `uncertain`. No candidate is
  promoted to a proven call or table access.

The source ingestion caller that knows file/version IDs should adapt the
parser `Relation` values into `SourceCodeRelation` and call the repository
after all participating versions are staged. T10 intentionally does not
rewrite the existing shared publication flow; that wiring belongs to the
next business-flow/integration task and must preserve the fixed snapshot.

## Pending coordination with the integration task

The current branch deliberately has no caller from `datasource_source_sync`
or a public retrieval endpoint. Adding that caller would require a decision on
whether XML is admitted by the existing source manifest route and how the
isolated parser service's ADR-0008 boundary is extended. The current evidence
is the independently tested extractor, repository contract, and migration;
production wiring is a P1 integration gap and is not silently claimed as
complete here.

ADR-0008 boundary note: this branch keeps MyBatis XML/SQL as a narrow
auxiliary extractor using Go's standard XML tokenizer and a non-executing SQL
lexer; it does not replace the deployed Tree-sitter language parser. If the
integration task interprets ADR-0008 as forbidding even this auxiliary route,
the extractor must move behind the existing parser service before wiring. That
is a pending architecture decision, not a silent ADR override.
