# T15 b7bb894e config-evidence review

Reviewed clean helper freeze `b7bb894e6974ddce35cae37e68a5790746fc1a9d`, targeted delta after `d406a67d21e4853ae4ea653a036492bf1aaf57b6`. The user-approved full T15 baseline remains `999e9398b8e4f67d17ba8e03c3fc65ba4b15332a`. Only the four delegated source/types files are reviewed; consumer and diagram changes belong to separate pending slices.

## Standards

Independent Sol/high review: **2 P2 findings, no additional actionable smell judgments**.

1. `source_relation_fact_refs.go:121` reports persisted refs as verified after checking only that their kind, quality, range and file identity exist in the same snapshot. It does not bind them to the supplied relation. An unrelated same-snapshot prefix can therefore be presented as causal support, contrary to the structural-support domain rule in `CONTEXT.md:74` and the exact causal-ref contract in `internal/types/source_relation.go`.
2. `source_relations.go:576` omits a Spring class-mapping ref when the method's numeric range covers the class-mapping range, without checking file/version identity. Those coordinates are file-local; namespace correlation can involve distinct members. Require matching file/version/path before considering range coverage sufficient.

## Spec

Independent Sol/high review: **1 P2 finding**. The first Standards finding also violates T15 acceptance criterion 14 and the approved causal-evidence seam: configuration refs must support the specific verified relation, not merely refer to existing facts in its snapshot. Compare persisted refs with the complete causal-ref set from the unique exact relation replay, or an equivalent bounded causal validation. Missing, extra, unrelated or unavailable refs must fail closed. Legacy direct matches may still legitimately replay to zero configuration refs.

The axes remain separate: this is two distinct repairs, with the first reported on both axes. No helper-scope creep or additional missing feature was identified. The previously reviewed main/UI slices remain passed.

## Independent verification

- Root's focused actual Go run covering producer and resolver behavior passed, source package **4.002s**.
- Root appended two temporary public-seam counters to the frozen test file using a Go overlay, without modifying the worker tree. Both actually failed, package **3.224s**:
  - A matching route's prefix ref was replaced with the exact range of an unused `/unused` prefix in the same complete snapshot. The resolver returned `verified` instead of unavailable.
  - Class/method mappings were placed in separate members with equal file-local ranges. Existing correlation formed the joined route but omitted its causal class-mapping ref.
- These are pure correlation/resolver checks. They are not claimed as PostgreSQL, actual-parser, full-application or UI verification. The combined real-parser/PG configuration citation and pin test remains pending consumer/diagram completion.
- All root test processes were collected. The worker freeze remained clean.

## Disposition

**Helper not passed; T15/#23 remains open, accepted count 17/22.** Both exact repairs were sent to the original a8ea Luna/xhigh worker, preserving the exported interface and cached once-per-snapshot replay. Main consumer and pure diagram workers may continue candidate assembly in parallel, but the final T15 freeze must include the helper repair and pass combined verification. No root integration, issue closure or next-ticket dispatch was performed.
