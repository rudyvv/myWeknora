package types

const (
	SourceWikiContributionMaxCount              = 128
	SourceWikiContributionMaxBodyBytes          = 1 << 20
	SourceWikiContributionMaxTotalBodyBytes     = 4 << 20
	SourceWikiContributionMaxEvidencePerItem    = 256
	SourceWikiContributionMaxFailureReasonBytes = 4096
)

type SourceWikiContributionState string

const (
	SourceWikiContributionCurrent SourceWikiContributionState = "current"
	SourceWikiContributionStale   SourceWikiContributionState = "stale"
)

type SourceWikiContribution struct {
	SourceID             string                      `json:"source_id"`
	TopicKind            string                      `json:"topic_kind"`
	TopicKey             string                      `json:"topic_key"`
	OriginSnapshotID     string                      `json:"origin_snapshot_id"`
	ApplicableSnapshotID string                      `json:"applicable_snapshot_id"`
	Body                 string                      `json:"body"`
	Evidence             []SourceWikiEvidence        `json:"evidence"`
	State                SourceWikiContributionState `json:"state"`
	FailureReason        string                      `json:"failure_reason,omitempty"`
}

type SourceWikiContributionSet struct {
	TenantID            uint64                   `json:"tenant_id"`
	KnowledgeBaseID     string                   `json:"knowledge_base_id"`
	Contributions       []SourceWikiContribution `json:"contributions"`
	HasUnattributedBody bool                     `json:"has_unattributed_body"`
}

type SourceWikiContributionTarget struct {
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	SourceID        string `json:"source_id"`
	TopicKind       string `json:"topic_kind"`
	TopicKey        string `json:"topic_key"`
	SnapshotID      string `json:"snapshot_id"`
}

type SourceWikiContributionOutcomeKind string

const (
	SourceWikiContributionOutcomeReplace          SourceWikiContributionOutcomeKind = "replace"
	SourceWikiContributionOutcomeUnchanged        SourceWikiContributionOutcomeKind = "unchanged"
	SourceWikiContributionOutcomeGenerationFailed SourceWikiContributionOutcomeKind = "generation_failed"
	SourceWikiContributionOutcomeConfirmedDeleted SourceWikiContributionOutcomeKind = "confirmed_deleted"
)

type SourceWikiContributionOutcome struct {
	Kind               SourceWikiContributionOutcomeKind `json:"kind"`
	Replacement        *SourceWikiContribution           `json:"replacement,omitempty"`
	FailureReason      string                            `json:"failure_reason,omitempty"`
	CompleteSnapshotID string                            `json:"complete_snapshot_id,omitempty"`
}
