package types

import "time"

const (
	SourceWikiSkeletonMaxFiles             = 50_000
	SourceWikiSkeletonMaxRelations         = 200_000
	SourceWikiImpactMaxFacts               = 200_000
	SourceWikiImpactMaxFactBytes           = 64 << 20
	SourceWikiImpactMaxContextBytes        = 32 << 20
	SourceWikiImpactMaxRelationBytes       = 32 << 20
	SourceWikiImpactMaxMemberMetadataBytes = 32 << 20
	SourceWikiBatchMaxCalls                = 240
	SourceWikiBatchMaxTokens               = 4_000_000
	SourceWikiBatchMaxElapsed              = time.Hour
	SourceWikiBatchQAMaxElapsed            = 5 * time.Minute
	SourceWikiBatchMaxInitialTopics        = 40
	SourceWikiBatchMaxCompletionTokens     = 4096
	SourceWikiBatchMinInputTokens          = 4096
	SourceWikiBatchMaxCandidates           = 100000
	SourceWikiBatchSkeletonMaxCalls        = 6
	SourceWikiBatchSkeletonMaxTokens       = 120_000
	SourceWikiBatchQAMaxCalls              = 12
	SourceWikiBatchQAMaxTokens             = 240_000
	SourceWikiBatchChildMaxCalls           = 18
	SourceWikiBatchChildMaxTokens          = 360_000
)

type SourceWikiSkeletonFile struct {
	Path      string
	Generated bool
	Facts     []ParsedSourceFact
}

// SourceWikiTopic is a source-owned stable subject planned against one fixed
// snapshot. TopicKey omits SnapshotID; SourceID is its identity namespace.
type SourceWikiTopic struct {
	SourceID           string               `json:"source_id"`
	SnapshotID         string               `json:"snapshot_id"`
	TopicKey           string               `json:"topic_key"`
	Kind               string               `json:"kind"`
	ModulePath         string               `json:"module_path,omitempty"`
	Title              string               `json:"title"`
	Priority           int                  `json:"priority"`
	Status             string               `json:"status"`
	Uncertain          bool                 `json:"uncertain"`
	UncertaintyReasons []string             `json:"uncertainty_reasons,omitempty"`
	Relations          []SourceCodeRelation `json:"relations,omitempty"`
}

// SourceWikiBatch is an immutable-budget parent over a fixed published
// snapshot. DeadlineAt is absolute and is never extended by a resumed worker.
type SourceWikiBatch struct {
	ID                       string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID                 uint64     `json:"tenant_id"`
	KnowledgeBaseID          string     `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	SourceID                 string     `json:"source_id" gorm:"type:varchar(36);index"`
	SnapshotID               string     `json:"snapshot_id" gorm:"type:varchar(36);index"`
	SourceConfigFingerprint  string     `json:"-" gorm:"type:varchar(64)"`
	SourceUpdatedAt          time.Time  `json:"source_updated_at"`
	ModelID                  string     `json:"model_id" gorm:"type:varchar(36)"`
	ModelSettingsFingerprint string     `json:"-" gorm:"type:varchar(64)"`
	ModelContextWindow       int        `json:"model_context_window"`
	MaxCompletionTokens      int        `json:"max_completion_tokens"`
	Status                   string     `json:"status"`
	Phase                    string     `json:"phase"`
	CurrentTopicKey          string     `json:"current_topic_key,omitempty"`
	Cursor                   int        `json:"cursor"`
	QACursor                 int        `json:"qa_cursor"`
	PublishCursor            int        `json:"publish_cursor"`
	QADueAt                  *time.Time `json:"qa_deadline_at,omitempty" gorm:"column:qa_deadline_at"`
	QAApprovedAt             *time.Time `json:"qa_approved_at,omitempty"`
	QAApprovalDigest         string     `json:"-" gorm:"type:varchar(64);not null;default:''"`
	RevalidationAttemptID    string     `json:"-" gorm:"type:varchar(36);not null;default:''"`
	CandidateCount           int        `json:"candidate_count"`
	InitialCount             int        `json:"initial_count"`
	CallsReserved            int        `json:"calls_reserved"`
	TokensReserved           int        `json:"tokens_reserved"`
	SkeletonCallsReserved    int        `json:"skeleton_calls_reserved"`
	SkeletonTokensReserved   int        `json:"skeleton_tokens_reserved"`
	QACallsReserved          int        `json:"qa_calls_reserved" gorm:"column:qa_calls_reserved"`
	QATokensReserved         int        `json:"qa_tokens_reserved" gorm:"column:qa_tokens_reserved"`
	MaxCalls                 int        `json:"max_calls"`
	MaxTokens                int        `json:"max_tokens"`
	MaxElapsedMS             int64      `json:"max_elapsed_ms"`
	MaxInitialTopics         int        `json:"max_initial_topics"`
	SkeletonMaxCalls         int        `json:"skeleton_max_calls"`
	SkeletonMaxTokens        int        `json:"skeleton_max_tokens"`
	QAMaxCalls               int        `json:"qa_max_calls" gorm:"column:qa_max_calls"`
	QAMaxTokens              int        `json:"qa_max_tokens" gorm:"column:qa_max_tokens"`
	DeadlineAt               time.Time  `json:"deadline_at"`
	Reason                   string     `json:"reason,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	FinishedAt               *time.Time `json:"finished_at,omitempty"`
}

func (SourceWikiBatch) TableName() string { return "source_wiki_batches" }

// SourceWikiCoverageTopic is the latest planned/attempted state for a stable
// source topic. WikiSlug remains populated for ready and historical cards so
// existing directory links continue to resolve.
type SourceWikiCoverageTopic struct {
	ID                  string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID            uint64    `json:"tenant_id"`
	KnowledgeBaseID     string    `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	SourceID            string    `json:"source_id" gorm:"type:varchar(36);uniqueIndex:source_wiki_topic_identity"`
	TopicKey            string    `json:"topic_key" gorm:"type:text;uniqueIndex:source_wiki_topic_identity"`
	SnapshotID          string    `json:"snapshot_id" gorm:"type:varchar(36);index"`
	Kind                string    `json:"kind"`
	ModulePath          string    `json:"module_path,omitempty"`
	Title               string    `json:"title"`
	Priority            int       `json:"priority"`
	Initial             bool      `json:"initial"`
	Status              string    `json:"status"`
	Uncertain           bool      `json:"uncertain"`
	UncertaintyReasons  JSON      `json:"uncertainty_reasons" gorm:"type:jsonb"`
	Relations           JSON      `json:"relations" gorm:"type:jsonb"`
	BatchID             *string   `json:"batch_id,omitempty" gorm:"type:varchar(36);index"`
	AttemptID           *string   `json:"attempt_id,omitempty" gorm:"type:varchar(36);index"`
	WikiSlug            string    `json:"wiki_slug,omitempty"`
	LastReadySnapshotID string    `json:"last_ready_snapshot_id,omitempty" gorm:"type:varchar(36)"`
	Reason              string    `json:"reason,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (SourceWikiCoverageTopic) TableName() string { return "source_wiki_topics" }

// SourceWikiBatchCallReservation is the parent-level durable charge. Card
// calls mirror T17's per-attempt reservation in the same DB transaction;
// skeleton and whole-batch QA use this ledger directly.
type SourceWikiBatchCallReservation struct {
	ID               string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	BatchID          string     `json:"batch_id" gorm:"type:varchar(36);index"`
	AttemptID        *string    `json:"attempt_id,omitempty" gorm:"type:varchar(36);index"`
	AttemptCallID    *string    `json:"attempt_call_id,omitempty" gorm:"type:varchar(36);uniqueIndex"`
	Phase            string     `json:"phase"`
	ProviderPhase    string     `json:"provider_phase,omitempty"`
	ReservedTokens   int        `json:"reserved_tokens"`
	ExpectedQACursor *int       `json:"expected_qa_cursor,omitempty"`
	ActualTokens     *int       `json:"actual_tokens,omitempty"`
	Outcome          string     `json:"outcome"`
	CreatedAt        time.Time  `json:"created_at"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

func (SourceWikiBatchCallReservation) TableName() string { return "source_wiki_batch_calls" }

type SourceWikiBatchReserveCallRequest struct {
	BatchID          string
	AttemptID        string
	AttemptCallID    string
	Phase            string
	ProviderPhase    string
	ReservedTokens   int
	ExpectedQACursor *int
	Now              time.Time
}

// SourceWikiBatchPreflightRequest may identify a terminal batch whose work is
// being planned again. Expected bindings prevent a stale UI preview from
// silently starting against a newer source or model configuration.
type SourceWikiBatchPreflightRequest struct {
	RestartOfBatchID        string    `json:"restart_of_batch_id,omitempty"`
	ExpectedSnapshotID      string    `json:"expected_snapshot_id,omitempty"`
	ExpectedSourceUpdatedAt time.Time `json:"expected_source_updated_at,omitempty"`
	ExpectedModelID         string    `json:"expected_model_id,omitempty"`
	ExpectedModelUpdatedAt  time.Time `json:"expected_model_updated_at,omitempty"`
}

type SourceWikiBatchRestartReference struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	SnapshotID     string `json:"snapshot_id"`
	Phase          string `json:"phase"`
	Cursor         int    `json:"cursor"`
	CandidateCount int    `json:"candidate_count"`
	InitialCount   int    `json:"initial_count"`
	CallsReserved  int    `json:"calls_reserved"`
	TokensReserved int    `json:"tokens_reserved"`
}

// SourceWikiBatchPreflight is a read-only server-side check and bounded plan
// preview. It is deliberately not a batch row and cannot dispatch provider
// calls. PlannedTopics and fingerprints are retained only for a future
// accepted runner integration and are not serialized to clients.
type SourceWikiBatchPreflight struct {
	PreflightPassed          bool                             `json:"preflight_passed"`
	StartAvailable           bool                             `json:"start_available"`
	DispatchReason           string                           `json:"dispatch_reason,omitempty"`
	SourceID                 string                           `json:"source_id"`
	SnapshotID               string                           `json:"snapshot_id"`
	CommitSHA                string                           `json:"commit_sha"`
	PublishedAt              *time.Time                       `json:"published_at,omitempty"`
	SourceUpdatedAt          time.Time                        `json:"source_updated_at"`
	ModelID                  string                           `json:"model_id"`
	ModelUpdatedAt           time.Time                        `json:"model_updated_at"`
	ModelContextWindow       int                              `json:"model_context_window"`
	ModelContextKnown        bool                             `json:"model_context_known"`
	MaxCompletionTokens      int                              `json:"max_completion_tokens"`
	CandidateCount           int                              `json:"candidate_count"`
	InitialCount             int                              `json:"initial_count"`
	ExpansionCount           int                              `json:"expansion_count"`
	ModuleCount              int                              `json:"module_count"`
	FlowCount                int                              `json:"flow_count"`
	InitialTopics            []SourceWikiTopic                `json:"initial_topics"`
	RestartFrom              *SourceWikiBatchRestartReference `json:"restart_from,omitempty"`
	Warnings                 []string                         `json:"warnings,omitempty"`
	MaxCalls                 int                              `json:"max_calls"`
	MaxTokens                int                              `json:"max_tokens"`
	MaxElapsedMS             int64                            `json:"max_elapsed_ms"`
	MaxInitialTopics         int                              `json:"max_initial_topics"`
	SkeletonMaxCalls         int                              `json:"skeleton_max_calls"`
	SkeletonMaxTokens        int                              `json:"skeleton_max_tokens"`
	QAMaxCalls               int                              `json:"qa_max_calls"`
	QAMaxTokens              int                              `json:"qa_max_tokens"`
	SourceConfigFingerprint  string                           `json:"-"`
	ModelSettingsFingerprint string                           `json:"-"`
	PlannedTopics            []SourceWikiTopic                `json:"-"`
}
