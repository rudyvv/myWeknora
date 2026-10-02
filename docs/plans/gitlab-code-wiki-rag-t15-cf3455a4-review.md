# T15 cf3455a4 final candidate review

Frozen combined candidate `cf3455a430716b8764aece436026f74415258993`, user-approved full-ticket baseline `999e9398b8e4f67d17ba8e03c3fc65ba4b15332a`. The previously passed main/UI repairs remain part of this candidate. Configuration repair `a2cdc94a` and diagram repair `e0e82c1d` were cherry-picked as `96f47f60` and `1b56e302`; root verified both stable patch IDs match their respective originals. All worker trees were frozen; no next ticket was assigned during review.

## Standards

Independent GPT-6 Sol/high axis: **0 documented-standard or ADR violations; 1 new nonblocking P3 judgement call**. Possible Duplicated Code: `source_wiki_flow_diagram.go:294` and `source_relation_fact_refs.go:211` independently encode the causal role-to-fact-kind mapping. Sharing a validator would prevent a future role addition from diverging between verification and diagram construction. The previously recorded three P3 suggestions remain nonblocking. The earlier two P2 causal-reference defects are repaired.

## Spec

Independent GPT-6 Sol/high axis: **0 new findings**. Producer refs describe facts that participate in the exact route; complete bounded snapshot replay checks the unique full relation identity and the entire causal-ref set. Range suppression requires the same file/version/path. Legacy direct routes can legitimately replay with zero refs; incomplete or unrelated refs fail closed. Preflight persists hydrated relations, collection reads exact configuration windows, and candidate checkpoints, diagrams, complete-set QA and durable evidence owners preserve their identities. Prior six initial findings and the subsequent late-range/manual/UI/configuration findings have been resolved across the documented repair reviews.

## Independent verification

- Root actual pure Go checks: source resolver/producer, including both former root counters, **PASS 3.415s**; consumer, diagrams and skeleton, **PASS 6.465s**.
- Root actual dedicated PostgreSQL run: **5/5 PASS**, package **81.462s**. At least twenty cards generated and QAed (36.97s); rejected whole-batch QA never exposes drafts (13.46s); cross-file configuration refs reject wrong member/version/source (10.00s); HTTP preflight/start remains scoped and persists the batch (4.10s).
- Root independently added a temporary real-parser/PG legacy-snapshot counter: reset persisted route contexts to `[]`, replay a route across four files with prefix/proxy facts after byte 2048, require exactly prefix/proxy/class-mapping refs, preserve uncertain determinacy, cite every required exact evidence ID in the graph, and verify each configuration version has a durable owner pin. **PASS 12.91s**. It used a Go overlay, leaving the candidate tree clean.
- The first root draft overlay had an obsolete method name; corrected to the existing `loadOrCreateSourceWikiAttempt` seam before actual execution. An unnecessary second overlay duplicated two tests already adopted by the worker; root ran their native versions instead. These were root test-setup errors, not product regressions.
- Prior independent late-range/manual/UI checks are recorded in `gitlab-code-wiki-rag-t15-db8a1d6c-review.md`; candidate UI six tests and typecheck also passed in the worker's combined tree. No full-suite, external-model quality or representative large-repository performance claim is made.
- Dedicated database: `127.0.0.1:57821/source_test`, existing local process-only environment script. No old/shared container credentials were read and no shared dependency was modified. All root test sessions were collected.

## Disposition

Candidate passes both axes and necessary independent checks. Root may integrate and perform bounded merge validation, then publish the T15 acceptance milestone and close #23. Only after that acceptance may the original a8ea Luna/xhigh chat start T16/#24 from the accepted integration SHA. T19 remains dependent on T16.
