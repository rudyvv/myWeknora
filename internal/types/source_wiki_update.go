package types

import "time"

// SourceWikiUpdatePlan is the durable, bounded result of comparing two
// complete source snapshots. It contains no generated model output.
type SourceWikiUpdatePlan struct {
	ID                 string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64     `json:"tenant_id"`
	KnowledgeBaseID    string     `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	SourceID           string     `json:"source_id" gorm:"type:varchar(36)"`
	PreviousSnapshotID string     `json:"previous_snapshot_id" gorm:"type:varchar(36)"`
	SnapshotID         string     `json:"snapshot_id" gorm:"type:varchar(36)"`
	ConfigGeneration   int64      `json:"config_generation"`
	PlanDigest         string     `json:"plan_digest" gorm:"type:varchar(64)"`
	Status             string     `json:"status"`
	SourceWideStale    bool       `json:"source_wide_stale"`
	ReasonCode         string     `json:"reason_code"`
	Reason             string     `json:"reason"`
	Plan               JSON       `json:"plan" gorm:"type:jsonb"`
	NextInventory      JSON       `json:"next_inventory" gorm:"type:jsonb"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

func (SourceWikiUpdatePlan) TableName() string { return "source_wiki_update_plans" }

// SourceWikiUpdatePlanItem is one deterministic topic action selected by a
// persisted update plan. ExpectedPageVersion is the page CAS fence; workers
// may never write a candidate generated for an older page or snapshot.
type SourceWikiUpdatePlanItem struct {
	ID                    int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	PlanID                string    `json:"plan_id" gorm:"type:varchar(36);index"`
	TopicKey              string    `json:"topic_key"`
	PageID                *string   `json:"page_id,omitempty" gorm:"type:varchar(36)"`
	Action                string    `json:"action"`
	ReasonCode            string    `json:"reason_code"`
	Reason                string    `json:"reason"`
	State                 string    `json:"state"`
	ExpectedPageVersion   int       `json:"expected_page_version"`
	EvidenceSHA256        string    `json:"evidence_sha256" gorm:"type:varchar(64)"`
	DependencyFingerprint string    `json:"dependency_fingerprint" gorm:"type:varchar(64)"`
	ModuleFingerprint     string    `json:"module_fingerprint" gorm:"type:varchar(64)"`
	Details               JSON      `json:"details" gorm:"type:jsonb"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (SourceWikiUpdatePlanItem) TableName() string { return "source_wiki_update_plan_items" }

// SourceWikiPageContribution records the independently replaceable source
// contribution to a Wiki page/revision. The single-source legacy provenance
// field remains as a read-compatible projection; it is never used to infer
// ownership for mixed pages.
type SourceWikiPageContribution struct {
	ID                    int64     `json:"id" gorm:"primaryKey;autoIncrement"`
	PageID                string    `json:"page_id" gorm:"type:varchar(36);index"`
	RevisionID            *string   `json:"revision_id,omitempty" gorm:"type:varchar(36);index"`
	SourceID              string    `json:"source_id" gorm:"type:varchar(36);index"`
	PageVersion           int       `json:"page_version"`
	TopicKind             string    `json:"topic_kind"`
	TopicKey              string    `json:"topic_key"`
	ApplicableSnapshotID  string    `json:"applicable_snapshot_id" gorm:"type:varchar(36)"`
	TargetSnapshotID      string    `json:"target_snapshot_id" gorm:"type:varchar(36)"`
	State                 string    `json:"state"`
	ReasonCode            string    `json:"reason_code"`
	Reason                string    `json:"reason"`
	EvidenceSHA256        string    `json:"evidence_sha256" gorm:"type:varchar(64)"`
	DependencyFingerprint string    `json:"dependency_fingerprint" gorm:"type:varchar(64)"`
	ModuleFingerprint     string    `json:"module_fingerprint" gorm:"type:varchar(64)"`
	DependencyFileIDs     JSON      `json:"dependency_file_ids" gorm:"type:jsonb"`
	ModuleMemberFileIDs   JSON      `json:"module_member_file_ids" gorm:"type:jsonb"`
	Contribution          JSON      `json:"contribution" gorm:"type:jsonb"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func (SourceWikiPageContribution) TableName() string { return "source_wiki_page_contributions" }
