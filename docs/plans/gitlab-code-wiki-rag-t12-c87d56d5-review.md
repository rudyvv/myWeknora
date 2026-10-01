# T12 c87d56d5 review

Frozen clean commit: `c87d56d521f3c34a3e344f35dbde4eb6053fc2df`; user-approved review base: `9e2ef8261d29f0d3fb74916de1d8e34dff58b594`. Standards and Spec were reviewed independently by GPT-6 Sol/high. The interrupted Spec review was resumed without repeating the completed Standards review.

## Standards

Documented-standard violations: 0. Two P3 judgement calls: profile-identity guards repeat in `datasource_source_incremental.go`, and the fallback extension allowlists must be maintained in both Go and Python. Neither blocks acceptance.

## Spec

One blocking P2 remains: `PreviewSource` accepts a constructible embedding profile and reports `CanSync=true` even when the selected path's unavoidable index header cannot fit. `NewIndexProfile` can admit a 17-token input limit with only one usable token; parsing then rejects the path header. The integration test explicitly expects preview success followed by sync failure. The parser's 64-byte minimum chunk can likewise make some small admitted profiles unusable. This conflicts with Spec line 84 (actual model budget including header) and implementation-plan lines 73/98 (capability/range preview and actual model limits).

Required fix: preflight included paths against the unavoidable header and minimum feasible body, show the path/profile as non-ready in preview, and retain fail-closed synchronization with the old publication intact. Original evidence/path must not be silently truncated. Unsupported UTF-16 is an agreed explicit-failure boundary and is not a finding.

## Independent verification

Root reran two real MyBatis HTTP boundary cases: 2/2 passed. Go source package passed. Three isolated PostgreSQL cases passed in 30.298s: large Java/Mapper/fallback publication with dual-index retrieval, positive dimension readiness, and unfit-header failure preserving the old publication. The branch stayed clean at the frozen SHA until these commands completed. The last case confirms preservation, but also confirms the misleading preview covered by the P2.

The finding was returned to the original Luna/xhigh execution chat. Do not integrate, close #20, or dispatch its successor until the new clean commit passes review and necessary independent verification.
