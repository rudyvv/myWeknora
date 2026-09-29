# T08 Python structure and source-RAG validation

Ticket: [GitHub #16](https://github.com/rudyvv/myWeknora/issues/16). Branch: `codex/source-python`, based on `3adaa587d16651a6d48f123d73c1e5cceaf75271`.

## Scope

This ticket adds the locked `tree-sitter-language-pack` Python grammar to the existing content/hash-only parser boundary. `.py` files use the same fixed-commit source snapshot, dual-index staging, publication, read-only source evidence and Agent tool path as the already accepted languages. Python extraction is deliberately syntactic: module, class, function/method, async signature and decorator ranges are preserved, while framework-specific decorator meaning is not inferred and repository code is never imported or executed.

The Python grammar was prefetched into a new cache separate from the previously accepted four-language cache. The existing cache remains read-only and unchanged. No database migration is required; the existing processing fingerprint incorporates the verified grammar set and extraction rules.

## Red / green evidence

Initial red command (before Python registration):

```text
D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe -m unittest sourceparser.tests.test_python_http_contract -v
```

The Python grammar was not advertised by `/health` and the Python contract could not pass. After registration, the same command passed 4 tests.

Green parser contract:

```text
$env:SOURCE_PARSER_CACHE=C:/Users/28211/.codex/worktrees/a8ea/WeKnora/.source-parser-python-grammar-locked
D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe -m unittest discover -s sourceparser/tests
```

Result: PASS. The suite covers Java, JavaScript, TypeScript, TSX and Python HTTP contracts; the Python-specific cases cover health/lock identity, class and method parent structure, decorators, async, Unicode, CRLF, syntax-error degradation, exact chunk coverage and extension admission. The same suite with the original four-language cache skips Python-specific tests and keeps the accepted-language regression green.

Results: five-language cache `22 tests, OK`; original four-language cache `18 tests, OK (1 skipped)`.

Go routing check:

```text
go test ./internal/source -run TestLanguageForPathIncludesLockedPythonGrammar -count=1
```

Result: PASS. `.py` and case variants route to `python`; existing Java/JS/TSX routes and unsupported extensions remain unchanged.

`go test ./internal/source -count=1`: PASS. The focused DataSource service regression (`TestSourceSettingsAreVersioned`, `TestSourceCredentialsCanRotate`): PASS. A broader `go test ./internal/application/service -run 'Test(DataSource|Source)'` compiled and reached the tests but retained two known Windows SQLite TempDir cleanup failures (`TestDataSourceServiceDeleteSQLiteCleansUpAfterSoftDelete`, `TestDataSourceServiceDeleteKeepsCleanupStateWhenSoftDeleteFails`); neither touches T08 files or Python behavior.

## Public integration evidence

The build-tagged source integration fixture now has a Python-specific test using the public DataSource service, the real parser HTTP boundary, local Git, independent PostgreSQL schema, both PostgreSQL retrieval routes, the source-aware grep Agent tool and the published source read view. It is run with the new five-language cache:

```text
$env:SOURCE_TEST_POSTGRES_DSN=<dedicated source_test DSN>
$env:SOURCE_TEST_PYTHON=D:/Project-Weknora/WeKnora/.source-parser-venv/Scripts/python.exe
$env:SOURCE_PARSER_CACHE=C:/Users/28211/.codex/worktrees/a8ea/WeKnora/.source-parser-python-grammar-locked
go test -tags=integration ./internal/application/service -run TestSourcePythonSnapshotPublishesIndexesAndReadView -count=1 -timeout 15m
```

Result: PASS, 10.697s. The fixture published the Python snapshot from a real local Git repository, found `reserve_booking` through both keyword and vector routes, found it through the source-aware grep tool, and returned the original CRLF/Unicode file through the source read view with the fixed source symbol identity. It used a generated independent schema in the dedicated `source_test` database; the shared container was not restarted or removed.

## Files and boundaries

- `sourceparser/runtime.py`, `prefetch.py`, `server.py`: add and verify Python without changing the HTTP request shape or allowing path access.
- `sourceparser/tests/test_python_http_contract.py`: independent Python corpus and public HTTP assertions.
- `internal/source/parser_client.go`: `.py` route; all range/hash/chunk validation remains shared.
- `internal/application/service/datasource_source.go` and `datasource_source_sync.go`: Python readiness and admission wording only.
- `internal/source/parser_client_test.go`: route regression.
- No migration and no changes to publication manifest semantics, MyBatis/SQL, failure reconciliation, global publication state or other ticket ownership.

## Limits

This ticket does not claim complete Python framework semantics, runtime import behavior, type resolution, dependency injection, dynamic dispatch or decorator execution. It does not claim representative-repository relevance or performance. Real GitLab, embedding/model and queue services remain controlled boundaries in the public integration fixture.
