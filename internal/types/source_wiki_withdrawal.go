package types

// SourceWikiProjectionKind identifies the exact page owner whose contribution
// projection is being withdrawn. It is explicit so a revision can never fall
// through to current-page behaviour.
type SourceWikiProjectionKind string

const (
	SourceWikiProjectionCurrent  SourceWikiProjectionKind = "current"
	SourceWikiProjectionRevision SourceWikiProjectionKind = "revision"
)

// SourceWikiWithdrawalProjection is the visible page-level projection that
// must be proven to come entirely from its typed entries before it is changed.
type SourceWikiWithdrawalProjection struct {
	Title      string                `json:"title"`
	Summary    string                `json:"summary"`
	Content    string                `json:"content"`
	SourceRefs []string              `json:"source_refs"`
	Provenance *SourceWikiProvenance `json:"source_provenance"`
}

// SourceWikiWithdrawalEntry pairs one complete source/topic contribution with
// the exact page metadata persisted for that contribution. Provenance is kept
// separately because the legacy page projection also preserves fields such as
// module_path that are not part of SourceWikiContribution.
type SourceWikiWithdrawalEntry struct {
	Contribution SourceWikiContribution `json:"contribution"`
	Title        string                 `json:"title"`
	Summary      string                 `json:"summary"`
	SourceRefs   []string               `json:"source_refs"`
	Provenance   SourceWikiProvenance   `json:"source_provenance"`
}

// SourceWikiWithdrawalInput is a complete, already-authorized projection for
// one current page or one retained revision. The caller must first establish
// the exact current/revision owner scope and verify that Entries covers every
// typed owner in that scope. It must also account for all evidence-owner refs
// and source refs: if a target-source owner cannot be attributed to an entry,
// set HasUnattributedBody so the helper withdraws the whole projection. An
// absent target entry is a no-op only after this completeness check; it is not
// proof by itself that the page or revision is unrelated.
type SourceWikiWithdrawalInput struct {
	TenantID            uint64                         `json:"tenant_id"`
	KnowledgeBaseID     string                         `json:"knowledge_base_id"`
	PageID              string                         `json:"page_id"`
	SourceID            string                         `json:"source_id"`
	ProjectionKind      SourceWikiProjectionKind       `json:"projection_kind"`
	HasUnattributedBody bool                           `json:"has_unattributed_body"`
	Page                SourceWikiWithdrawalProjection `json:"page"`
	Entries             []SourceWikiWithdrawalEntry    `json:"entries"`
}

type SourceWikiWithdrawalDisposition string

const (
	SourceWikiWithdrawalUnchanged    SourceWikiWithdrawalDisposition = "unchanged"
	SourceWikiWithdrawalReproject    SourceWikiWithdrawalDisposition = "reproject"
	SourceWikiWithdrawalWithdrawPage SourceWikiWithdrawalDisposition = "withdraw_page"
)

type SourceWikiWithdrawalReason string

const (
	SourceWikiWithdrawalReasonNone                    SourceWikiWithdrawalReason = ""
	SourceWikiWithdrawalReasonUnattributed            SourceWikiWithdrawalReason = "unattributed_body"
	SourceWikiWithdrawalReasonInvalidEntries          SourceWikiWithdrawalReason = "invalid_entries"
	SourceWikiWithdrawalReasonProjectionMismatch      SourceWikiWithdrawalReason = "projection_mismatch"
	SourceWikiWithdrawalReasonNoSurvivingContribution SourceWikiWithdrawalReason = "no_surviving_contribution"
	SourceWikiWithdrawalReasonHistoricalOwner         SourceWikiWithdrawalReason = "historical_owner"
)

// SourceWikiWithdrawalResult contains either an unchanged complete projection,
// a current-page projection rebuilt from surviving entries, or a whole-page
// withdrawal decision. WithdrawPage results never expose partial survivors.
type SourceWikiWithdrawalResult struct {
	Disposition SourceWikiWithdrawalDisposition `json:"disposition"`
	Reason      SourceWikiWithdrawalReason      `json:"reason,omitempty"`
	Page        *SourceWikiWithdrawalProjection `json:"page,omitempty"`
	Entries     []SourceWikiWithdrawalEntry     `json:"entries,omitempty"`
}
