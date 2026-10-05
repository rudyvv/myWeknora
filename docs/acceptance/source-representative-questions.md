# Source-representative questions

`source-representative-questions.json` is a first-release, human-review draft for the T22 evidence benchmark. It contains exactly 30 questions: 10 symbol/path, 10 business-chain, and 10 frontend/SQL. The dataset explicitly declares `hash_mode: "sha256_utf8_lf"`: each `sha256` is computed over the whole file encoded as UTF-8 after CRLF and CR line endings are normalized to LF. It is not a raw-byte hash. Each source locator also records its repository, relative path, inclusive 1-based line range, symbol/anchor text, and a short rationale. It stores no copied source spans, retrieved hits, expected answers, match labels, recall figures, or performance measurements.

The question set covers `getPushSchedule`, FreeTutor flows, large Java and Mapper files, and Vue 2 frontend code. The three representative projects contain no tracked TypeScript or Python files at their pinned snapshots. To cover those first-release parser cases without misrepresenting project contents, one TypeScript and one Python question are explicitly labeled `approved_supplement` and point to the approved test corpus in this repository. Those two questions are not evidence that the three representative projects contain TypeScript or Python.

The current status is `draft_for_human_confirmation`; a human must confirm question scope and wording before the benchmark is treated as accepted. This dataset and validator establish source-grounded prompts and valid locators only. They do not run ingestion or retrieval, measure Recall, or make 27/30 (or any other hit count) a completion criterion. Any later acceptance report must use the agreed runner's versioned report contract and actual retrieval output. A static file inventory is only a candidate scope; the production filter preview remains authoritative. Do not infer token totals or evaluation ETA from inventory counts.

## Validate

The validator uses only the Python standard library and checks a strict allowlist at the dataset, repository, question, and evidence levels, category counts, repository metadata, safe in-root paths, pinned Git revisions (where enabled), hashes according to the declared mode, line ranges, and symbol localization. Unknown fields are rejected, including result fields and source bodies such as `raw_source` or `full_source`. Cross-repository evidence remains supported through the optional evidence-level `repository` field. It needs no model, database, PostgreSQL, or product service. Supply each source repository root:

```powershell
python tools/acceptance/validate_source_representative_questions.py docs/acceptance/source-representative-questions.json --source-root nsb=D:\Project4-evip\code\nsb --source-root evip_mobile=D:\Project4-evip\code\evip_mobile --source-root evip_dashboard=D:\Project4-evip\code\evip-dashboard --source-root weknora_approved_test_supplement=.
```

The representative project roots must be checked out at the commits recorded in the JSON. The approved-supplement entry records the WeKnora baseline commit, but intentionally does not require the current checkout's `HEAD` to remain there; its cited files are still checked by exact file hash and line/symbol localization.

Run the independent standard-library tests with:

```powershell
python -m unittest tests.acceptance.test_source_representative_questions -v
```
