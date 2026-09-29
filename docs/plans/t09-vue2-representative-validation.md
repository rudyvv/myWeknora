# T09 representative Vue 2 validation

This records the opt-in acceptance run for the three user-selected Vue 2 components. The run read the files in memory, copied them only into the integration harness's temporary local Git fixture, and removed that fixture and its isolated PostgreSQL schema through test cleanup. No representative source, credentials, or SQL were added to this repository or emitted in the saved test report. The project itself was not built and no project plugins were run.

## Source identity and parser summary

Project Git `HEAD`: `15d9575ebea6d82d9bb1b69dfe2b9950c6eb4cd5`

Temporary fixture Git commit: `0deb62858d429af48a14d5750eee56caf20cc578`

Parser: `source-pack-1.19.0-rules-4-157ac577d0b6037f6ed86429cac6b77b`

| Component | SHA-256 | Bytes | CRLF | CJK characters | Chunks | Symbols | Verified coordinates | Script imports | Call-shaped function bodies |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `index.vue` | `35de0de6307d926d558679e20efd80ac87ce30b79511d2b49a0b5ec902c6d695` | 6,303 | 210 | 94 | 7 | 13 | 20 | 0 | 3 |
| `list.vue` | `a03657141bc2adf2353043279ad9f0f976760a9f5391cf8615ef441af78fb647` | 9,558 | 302 | 134 | 7 | 15 | 22 | 0 | 4 |
| `qr.vue` | `3ba99b79659ccd8e6bc8a908a880f718857e4b4be553ff38f764adb1fd959667` | 9,083 | 321 | 216 | 7 | 17 | 24 | 3 | 3 |

The production Go parser client verified the parser hash and byte length for every file, exact original bytes for every chunk and symbol-signature range, one-based line coordinates, complete contiguous chunk coverage, and template/script region markers. The representative page files exposed call-shaped bodies; the QR component exposed script imports.

## Published retrieval and evidence

The opt-in integration test used the real sync and publication pipeline against a temporary schema in the dedicated local test database. It exercised the PostgreSQL keyword and vector indexes with controlled local embeddings, then used public `HybridSearch` and authorized `GetSourceFile` calls:

- Keyword route: 3/3 representative files returned.
- Vector route: 3/3 representative files returned.
- Public source-file reads: 6/6 matched the independently computed file SHA-256, byte length, and published commit.
- Model-facing evidence slices: 6/6 included source region, quality, bounded symbols, and line coordinates; no resolved local path was emitted.
- GORM integration logging remained silent, including queries whose production repository path requests debug logging.

## Reproduction

With the dedicated local integration database, locked parser runtime, and the user-provided representative directory available, set `SOURCE_TEST_POSTGRES_DSN`, `SOURCE_TEST_PARSER_URL`, `SOURCE_PARSER_CACHE`, and `SOURCE_TEST_VUE2_COMPONENT_DIR`, then run:

```powershell
go test -tags integration -run '^TestSourceVue2RepresentativeAcceptance$' -count=1 -v ./internal/application/service
```

The representative-directory variable is opt-in; the test skips when it is unset. The independent synthetic `lang="ts"` Vue regression remains in `TestSourceVueSFCRegionsPublishAndScopeExternalScriptResolution`.

## Result

Passed on 2026-09-29. The final representative run reported `keyword_files=3`, `vector_files=3`, `public_reads=6`, and `model_evidence_slices=6`. The formatter regression suite also passed with coverage for `knowledge_search`, `grep_chunks`, `list_knowledge_chunks`, and `wiki_read_source_doc`, including XML escaping and output bounds.
