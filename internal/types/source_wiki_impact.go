package types

// SourceWikiImpactSnapshotStage identifies which side of an impact comparison
// is supplied. The previous side must be published-complete; the candidate
// side can be preparing-complete or already published-complete when planning
// runs asynchronously after publication.
type SourceWikiImpactSnapshotStage string

const (
	SourceWikiImpactPublishedComplete SourceWikiImpactSnapshotStage = "published_complete"
	SourceWikiImpactPreparingComplete SourceWikiImpactSnapshotStage = "preparing_complete"
)

// SourceWikiImpactSnapshot is a caller-materialized complete source snapshot.
// It is a value object for pure planning: creating it performs no reads or
// publication, and all IDs are interpreted inside this exact source.
type SourceWikiImpactSnapshot struct {
	TenantID              uint64
	KnowledgeBaseID       string
	SourceID              string
	SnapshotID            string
	Stage                 SourceWikiImpactSnapshotStage
	ManifestComplete      bool
	ExpectedMemberCount   int
	RelationsComplete     bool
	ExpectedRelationCount int
	Members               []SourceWikiImpactMember
	Relations             []SourceCodeRelation
}

// SourceWikiImpactMember carries all bounded parser-authored facts for one
// manifest entry. Facts includes structural/module facts and semantic facts.
// ContentSHA is the immutable source-content digest; for an excluded entry it
// may be the manifest blob digest because no parsed evidence is available.
type SourceWikiImpactMember struct {
	Path              string
	SourceFileID      string
	FileVersionID     string
	ContentSHA        string
	Status            string
	Generated         bool
	ParserVersion     string
	Quality           string
	FactsComplete     bool
	ExpectedFactCount int
	Facts             []ParsedSourceFact
}

// SourceWikiImpactTopicInventory is the complete topic inventory for exactly
// one snapshot. Previous inventories include every published/manual/expansion
// topic; next inventories include every retained topic and every skeleton
// candidate, not only the current provider batch.
type SourceWikiImpactTopicInventory struct {
	TenantID            uint64
	KnowledgeBaseID     string
	SourceID            string
	SnapshotID          string
	Complete            bool
	ExpectedTopicCount  int
	ExpectedModuleCount int
	Topics              []SourceWikiImpactTopicDependencies
	ModuleInventory     []SourceWikiImpactModule
}

// SourceWikiImpactTopicDependencies describes a topic's complete source-file
// dependency closure. IDs refer only to the source and snapshot on the parent
// inventory; the planner resolves and revalidates every ID itself.
type SourceWikiImpactTopicDependencies struct {
	TopicKey                    string
	Kind                        string
	ModulePath                  string
	SourceOwned                 bool
	EvidenceSHA                 string
	ExpectedDependencyFileCount int
	DependencyFileIDs           []string
}

// SourceWikiImpactModule records complete module membership for the parent
// inventory. It is not an authorization or applicability assertion.
type SourceWikiImpactModule struct {
	Path              string
	ExpectedFileCount int
	FileIDs           []string
}

type SourceWikiImpactDisposition string

const (
	SourceWikiImpactPlanned         SourceWikiImpactDisposition = "planned"
	SourceWikiImpactSourceWideStale SourceWikiImpactDisposition = "source_wide_stale"
)

// SourceWikiImpactPlan is source- and snapshot-bound. A source-wide-stale
// disposition is an explicit fallback, never an assertion that omitted or
// unknown topics are unaffected.
type SourceWikiImpactPlan struct {
	TenantID           uint64
	KnowledgeBaseID    string
	SourceID           string
	PreviousSnapshotID string
	NextSnapshotID     string
	Disposition        SourceWikiImpactDisposition
	Affected           []SourceWikiAffectedTopic
	Unaffected         []SourceWikiUnaffectedTopic
	Removed            []SourceWikiRemovedTopic
	RescanSkeleton     bool
	RescanReasons      []string
	FallbackReason     string
}

type SourceWikiAffectedTopic struct {
	TopicKey string
	Reasons  []string
}

type SourceWikiUnaffectedTopic struct {
	TopicKey                 string
	ApplicabilityFingerprint string
	EvidenceSHA              string
}

type SourceWikiRemovedTopic struct {
	TopicKey string
	Reason   string
}
