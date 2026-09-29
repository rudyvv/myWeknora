# T08 Python structure and source-RAG validation

Ticket: [GitHub #16](https://github.com/rudyvv/myWeknora/issues/16). Branch: `codex/source-python`, based on `3adaa587d16651a6d48f123d73c1e5cceaf75271`.

## Scope

This ticket adds the locked `tree-sitter-language-pack` Python grammar to the existing content/hash-only parser boundary. `.py` files use the same fixed-commit source snapshot, dual-index staging, publication, read-only source evidence and Agent tool path as the already accepted languages. Python extraction is deliberately syntactic: module, class, function/method, async signature and decorator ranges are preserved, while framework-specific decorator meaning is not inferred and repository code is never imported or executed.

The Python grammar was prefetched into a new cache separate from the previously accepted four-language cache. The existing cache remains read-only and unchanged. Python extraction rules are v4 (the earlier four languages stay on v3), so health, parse responses, processing fingerprints and parse-artifact keys all move together on five-language workers. No database migration is required; existing v3 artifacts are not reused after the parser fingerprint changes.

## Red / green evidence

Initial implementation red command (before Python registration):

```text
D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe -m unittest sourceparser.tests.test_python_http_contract -v
```

The Python grammar was not advertised by `/health` and the Python contract could not pass. After registration, the original four Python contract tests passed.

Review-fix red command (before iterative traversal/import extraction):

```text
D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe -m unittest sourceparser.tests.test_python_http_contract.PythonHTTPContract.test_deep_expression_within_limits_preserves_every_original_byte sourceparser.tests.test_python_http_contract.PythonHTTPContract.test_import_forms_preserve_statement_ranges_and_scope_structure -v
```

The 1,100-operand expression returned HTTP 422 (`source parsing failed`) from the Python recursion failure, and the import contract returned no import symbols.

Green parser contract:

```text
$env:SOURCE_PARSER_CACHE=C:/Users/28211/.codex/worktrees/a8ea/WeKnora/.source-parser-python-grammar-locked
D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe -m unittest discover -s sourceparser/tests
```

Result: PASS. The suite covers Java, JavaScript, TypeScript, TSX and Python HTTP contracts. Python cases cover locked health identity, module/class/function/method parents, decorators, async, aliases and multi-item/multiline/relative/module-local imports, Unicode/CRLF coordinates, multiline triple-quoted strings with import/decorator lookalikes, syntax-error degradation, the 1,100-operand expression, complete original-byte chunk coverage, and the explicit 413 request limit. The same suite with the original four-language cache skips the Python contract class and keeps the accepted-language regression green.

Results: five-language cache `27 tests, OK`; four-language compatibility cache `18 tests, OK (1 skipped)`. The compatibility run used the same locked grammar bytes with Python excluded; pre-existing grammar caches were left unchanged. Its health contract remains rules v3.

Go routing check:

```text
go test ./internal/source -run TestLanguageForPathIncludesLockedPythonGrammar -count=1
```

Result: PASS. `.py` and case variants route to `python`; existing Java/JS/TSX routes and unsupported extensions remain unchanged.

`go test ./internal/source -count=1`: PASS. The focused DataSource service regression (`TestSourceSettingsAreVersioned`, `TestSourceCredentialsCanRotate`): PASS. A broader `go test ./internal/application/service -run 'Test(DataSource|Source)'` compiled and reached the tests but retained two known Windows SQLite TempDir cleanup failures (`TestDataSourceServiceDeleteSQLiteCleansUpAfterSoftDelete`, `TestDataSourceServiceDeleteKeepsCleanupStateWhenSoftDeleteFails`); neither touches T08 files or Python behavior.

`go test ./internal/application/service -run '^TestSourceParseArtifactKeyChangesWithParserVersionAndIsDeterministic$' -count=1`: PASS. Identical source bytes and settings produce a stable key, while a parser rules-version change produces a distinct key.

## Public integration evidence

The build-tagged source integration fixture now has a Python-specific test using the public DataSource service, the real parser HTTP boundary, local Git, independent PostgreSQL schema, both PostgreSQL retrieval routes, the source-aware grep Agent tool and the published source read view. It is run with the new five-language cache:

```text
$env:SOURCE_TEST_POSTGRES_DSN=<dedicated source_test DSN>
$env:SOURCE_TEST_PYTHON=D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe
$env:SOURCE_PARSER_CACHE=C:/Users/28211/.codex/worktrees/a8ea/WeKnora/.source-parser-python-grammar-locked
go test -tags=integration ./internal/application/service -run TestSourcePythonSnapshotPublishesIndexesAndReadView -count=1 -timeout 15m
```

Result: PASS, 12.218s on the separately prepared batch-two test database at `127.0.0.1:57521`. The fixture published Python from a real local Git repository, found `reserve_booking` through both keyword and vector routes and the source-aware grep tool, then found a syntax-error marker through both index routes. The syntax-error hit retained `quality=syntax_error`, the fixed Git commit and path; the public source read returned identical original bytes and quality, and an explicit file-version read returned that same commit/version/quality. The test used its generated independent schema and did not inspect credentials or alter the database container.

The parser-fingerprint repair additionally seeds a same-SHA parsed artifact under a rules-v3 worker version with its Python import symbol removed. The real source sync uses the same five-language grammar cache, produces a different rules-v4 artifact key, invokes the parser instead of reusing the stale artifact, persists the rebuilt rules-v4 result with the import symbol, and publishes that symbol in the public source read. Health and parse responses are asserted to expose the same rules-v4 fingerprint.

Existing source quality UI regression:

```text
node --import tsx --test src/components/SourceCodeView.test.ts
```

Result: PASS, 1 test. The existing source viewer rendered the syntax-error label and preserved the readable Unicode source. The checkout has no local frontend `node_modules`; the test used a temporary junction to the already installed read-only dependency tree at the original project path, then removed the junction. No files in that original project tree were modified.

## Files and boundaries

- `sourceparser/runtime.py`: use an explicit stack for Python AST traversal, preserving declaration ancestry without Python call-stack depth limits; extract `import_statement` and `import_from_statement` as ranged syntactic symbols.
- `sourceparser/tests/test_python_http_contract.py`: independent Python corpus and public HTTP assertions, including depth, imports, multiline strings and explicit request-limit response.
- `internal/application/service/datasource_source_integration_test.go`: publish and retrieve a syntax-error Python file through both indexes and a fixed-version source read.
- `frontend/src/components/SourceCodeView.test.ts`: assert the existing source quality UI shows the syntax-error state.
- `internal/source/parser_client.go`: `.py` route; all range/hash/chunk validation remains shared.
- `internal/application/service/datasource_source.go` and `datasource_source_sync.go`: Python readiness and admission wording only.
- `internal/source/parser_client_test.go`: route regression.
- No migration and no changes to publication manifest semantics, MyBatis/SQL, failure reconciliation, global publication state or other ticket ownership.

## Limits

This ticket does not claim complete Python framework semantics, runtime import behavior, type resolution, dependency injection, dynamic dispatch or decorator execution. Imports are syntax records only; no imports or target code are executed. It does not claim representative-repository relevance or performance. Real GitLab, embedding/model and queue services remain controlled boundaries in the public integration fixture. The complete repository suite was not rerun for this repair; the changed parser, Go client, Python publication/read contract and quality UI were tested directly.
