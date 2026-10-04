package types

import (
	"strconv"
	"time"
)

// SourceRange refers to original UTF-8 bytes [start,end), with one-based lines.
type SourceRange struct {
	StartByte int `json:"start_byte"`
	EndByte   int `json:"end_byte"`
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

type SourceContext struct {
	Text  string      `json:"text"`
	Range SourceRange `json:"range"`
}

// SourceRegion identifies a Vue single-file component block without claiming
// that templates, styles, or custom blocks were compiled or executed.
type SourceRegion struct {
	Kind           string `json:"kind"`
	Language       string `json:"language,omitempty"`
	Quality        string `json:"quality"`
	ExternalSource string `json:"external_source,omitempty"`
	ExternalStatus string `json:"external_status,omitempty"`
	ResolvedPath   string `json:"resolved_path,omitempty"`
}

type SourceSymbol struct {
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	QualifiedName  string          `json:"qualified_name"`
	Signature      string          `json:"signature"`
	SignatureRange SourceRange     `json:"signature_range"`
	Range          SourceRange     `json:"range"`
	Annotations    []SourceContext `json:"annotations"`
	Region         *SourceRegion   `json:"region,omitempty"`
}

// SourceDiagnostic is a bounded parser code anchored to an original source
// range. Its human-facing explanation is generated from the code, not parser
// error text.
type SourceDiagnostic struct {
	Code  string      `json:"code"`
	Range SourceRange `json:"range"`
}

type ParsedSourceChunk struct {
	Content     string             `json:"content"`
	Range       SourceRange        `json:"range"`
	Quality     string             `json:"quality"`
	Symbols     []string           `json:"symbols"`
	Context     []SourceContext    `json:"context"`
	Region      *SourceRegion      `json:"region,omitempty"`
	Diagnostics []SourceDiagnostic `json:"diagnostics,omitempty"`
}

// ParsedSourceFact is parser-authored syntax evidence. Its range always points
// into the original immutable file; semantic fields are not source context.
type ParsedSourceFact struct {
	Kind                 string      `json:"kind"`
	Name                 string      `json:"name,omitempty"`
	Namespace            string      `json:"namespace,omitempty"`
	RoutePath            string      `json:"route_path,omitempty"`
	HTTPMethod           string      `json:"http_method,omitempty"`
	HTTPMethods          []string    `json:"http_methods,omitempty"`
	HTTPMethodsSpecified bool        `json:"http_methods_specified,omitempty"`
	HTTPMethodsCertain   bool        `json:"http_methods_certain,omitempty"`
	StatementType        string      `json:"statement_type,omitempty"`
	StatementID          string      `json:"statement_id,omitempty"`
	MethodName           string      `json:"method_name,omitempty"`
	ParameterTypes       []string    `json:"parameter_types,omitempty"`
	SignatureCertain     bool        `json:"signature_certain,omitempty"`
	IsAbstract           bool        `json:"is_abstract,omitempty"`
	IsDefault            bool        `json:"is_default,omitempty"`
	Receiver             string      `json:"receiver,omitempty"`
	TypeName             string      `json:"type_name,omitempty"`
	SQL                  string      `json:"sql,omitempty"`
	ResultMapRefs        []string    `json:"result_map_refs,omitempty"`
	IncludeRefs          []string    `json:"include_refs,omitempty"`
	TargetNamespace      string      `json:"target_namespace,omitempty"`
	TargetName           string      `json:"target_name,omitempty"`
	OwnerKind            string      `json:"owner_kind,omitempty"`
	OwnerName            string      `json:"owner_name,omitempty"`
	ReferenceKind        string      `json:"reference_kind,omitempty"`
	SuperTypes           []string    `json:"super_types,omitempty"`
	Dynamic              bool        `json:"dynamic,omitempty"`
	Certainty            string      `json:"certainty,omitempty"`
	Reason               string      `json:"reason,omitempty"`
	Quality              string      `json:"quality"`
	Range                SourceRange `json:"range"`
	Text                 string      `json:"text,omitempty"`
}

type ParsedSourceDiagnostic struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Range   SourceRange `json:"range,omitempty"`
}

type ParsedSourceFile struct {
	ParserVersion string                   `json:"parser_version"`
	SHA256        string                   `json:"sha256"`
	ByteLength    int                      `json:"byte_length"`
	Encoding      string                   `json:"encoding"`
	Quality       string                   `json:"quality"`
	Symbols       []SourceSymbol           `json:"symbols"`
	Chunks        []ParsedSourceChunk      `json:"chunks"`
	Facts         []ParsedSourceFact       `json:"facts"`
	Diagnostics   []ParsedSourceDiagnostic `json:"diagnostics"`
}

// SourceFile's ID is also its stable KnowledgeID. Repository text is versioned
// independently of user-managed Knowledge tags and descriptions.
type SourceFile struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	DataSourceID    string `json:"data_source_id" gorm:"type:varchar(36)"`
	Path            string `json:"path" gorm:"type:text"`
}

type SourceFileVersion struct {
	ID            string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	SourceFileID  string    `json:"source_file_id" gorm:"type:varchar(36);index"`
	SnapshotID    string    `json:"snapshot_id" gorm:"type:varchar(36);index"`
	BlobSHA       string    `json:"blob_sha"`
	SHA256        string    `json:"sha256"`
	Content       []byte    `json:"-" gorm:"type:bytea"`
	Encoding      string    `json:"encoding"`
	ParserVersion string    `json:"parser_version"`
	Quality       string    `json:"quality"`
	Symbols       JSON      `json:"symbols" gorm:"type:jsonb"`
	Facts         JSON      `json:"facts" gorm:"type:jsonb"`
	Diagnostics   JSON      `json:"diagnostics" gorm:"type:jsonb"`
	CreatedAt     time.Time `json:"created_at"`
}

type SourceSnapshot struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	DataSourceID    string `json:"data_source_id" gorm:"type:varchar(36);index"`
	SyncLogID       string `json:"sync_log_id" gorm:"type:varchar(36);uniqueIndex"`
	ProjectID       string `json:"project_id"`
	CommitSHA       string `json:"commit_sha"`
	// DetectedCommitSHA and TargetCommitSHA are run-local status fields. They
	// deliberately do not become part of the immutable snapshot row: a source
	// failure may happen before a candidate snapshot is created, but its log
	// still needs to tell the operator which HEAD was observed (when available)
	// and which commit was being processed.
	DetectedCommitSHA         string     `json:"detected_commit_sha,omitempty" gorm:"-"`
	TargetCommitSHA           string     `json:"target_commit_sha,omitempty" gorm:"-"`
	PublicationChecked        bool       `json:"publication_checked" gorm:"-"`
	RepositoryURL             string     `json:"repository_url"`
	RulesVersion              string     `json:"rules_version"`
	ProcessingVersion         string     `json:"processing_version"`
	EmbeddingVersion          string     `json:"embedding_version"`
	PreviousSnapshotID        string     `json:"previous_snapshot_id"`
	PreviousCommitSHA         string     `json:"previous_commit_sha"`
	AddedCount                int        `json:"added_count"`
	ChangedCount              int        `json:"changed_count"`
	DeletedCount              int        `json:"deleted_count"`
	RenamedCount              int        `json:"renamed_count"`
	ParsedCount               int        `json:"parsed_count"`
	ReusedFileCount           int        `json:"reused_file_count"`
	ReusedChunkCount          int        `json:"reused_chunk_count"`
	EmbeddedChunkCount        int        `json:"embedded_chunk_count"`
	ReusedVectorCount         int        `json:"reused_vector_count"`
	State                     string     `json:"state"`
	ManifestComplete          bool       `json:"manifest_complete"`
	ManifestDigest            string     `json:"manifest_digest"`
	MemberCount               int        `json:"member_count"`
	FileCount                 int        `json:"file_count"`
	ChunkCount                int        `json:"chunk_count"`
	RelationCount             int        `json:"relation_count"`
	RelationsStaged           bool       `json:"relations_staged"`
	Error                     string     `json:"error,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	PublishedAt               *time.Time `json:"published_at,omitempty"`
	LastSuccessfulPublishedAt *time.Time `json:"last_successful_published_at,omitempty" gorm:"-"`
	PreviousPublishedAt       *time.Time `json:"previous_published_at,omitempty" gorm:"-"`
}

type SourceSnapshotMember struct {
	SnapshotID    string `json:"snapshot_id" gorm:"type:varchar(36);primaryKey"`
	Path          string `json:"path" gorm:"type:text;primaryKey"`
	SourceFileID  string `json:"source_file_id,omitempty" gorm:"type:varchar(36);index"`
	FileVersionID string `json:"file_version_id,omitempty" gorm:"type:varchar(36);index"`
	BlobSHA       string `json:"blob_sha"`
	Size          int64  `json:"size"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Encoding      string `json:"encoding,omitempty"`
	Generated     bool   `json:"generated"`
	Change        string `json:"change"`
	PreviousPath  string `json:"previous_path"`
	ParseReused   bool   `json:"parse_reused"`
}

type SourcePublication struct {
	DataSourceID    string `gorm:"type:varchar(36);primaryKey"`
	SnapshotID      string `gorm:"type:varchar(36);not null;index"`
	TenantID        uint64
	KnowledgeBaseID string `gorm:"type:varchar(36)"`
}

// SourceChunkReference is typed membership, independent of editable Chunk metadata.
type SourceChunkReference struct {
	ChunkID       string `gorm:"type:varchar(36);primaryKey"`
	SnapshotID    string `gorm:"type:varchar(36);index"`
	SourceFileID  string `gorm:"type:varchar(36);index"`
	FileVersionID string `gorm:"type:varchar(36);index"`
}

type SourceRunResult struct {
	Snapshot *SourceSnapshot        `json:"snapshot"`
	Members  []SourceSnapshotMember `json:"members"`
}

// SourceWikiUpdatePayload is the stable, DB-derived acceptance record for a
// published source snapshot. EventID identifies the publication; DeliveryID
// identifies its delivery for one source configuration generation. Neither
// field requests or contains generated Wiki content.
type SourceWikiUpdatePayload struct {
	SchemaVersion    int    `json:"schema_version"`
	EventID          string `json:"event_id"`
	DeliveryID       string `json:"delivery_id"`
	TenantID         uint64 `json:"tenant_id"`
	KnowledgeBaseID  string `json:"knowledge_base_id"`
	DataSourceID     string `json:"data_source_id"`
	SnapshotID       string `json:"snapshot_id"`
	CommitSHA        string `json:"commit_sha"`
	ConfigGeneration int64  `json:"config_generation"`
}

// SourceWikiUpdateTriggerPayload is the per-KB wake-up signal for the durable
// source:wiki:update lane. It intentionally carries no source or generation;
// the consumer claims exact accepted rows and revalidates each full payload.
type SourceWikiUpdateTriggerPayload struct {
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

type SourceWikiUpdateQueueScope struct {
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

// SourceWikiDeliveryID gives each configuration-generation delivery of a
// published snapshot a stable identity while keeping its publication EventID
// unchanged. The task queue uses this as its deduplication/ack identity.
func SourceWikiDeliveryID(eventID string, configGeneration int64) string {
	if eventID == "" || configGeneration <= 0 {
		return ""
	}
	return eventID + ":g" + strconv.FormatInt(configGeneration, 10)
}

type SourceEvidence struct {
	DataSourceID  string             `json:"data_source_id"`
	SnapshotID    string             `json:"snapshot_id"`
	FileVersionID string             `json:"file_version_id"`
	ProjectID     string             `json:"project_id"`
	CommitSHA     string             `json:"commit_sha"`
	Path          string             `json:"path"`
	Range         SourceRange        `json:"range"`
	Symbols       []string           `json:"symbols"`
	Quality       string             `json:"quality"`
	GitLabURL     string             `json:"gitlab_url"`
	Context       []SourceContext    `json:"context"`
	Region        *SourceRegion      `json:"region,omitempty"`
	Diagnostics   []SourceDiagnostic `json:"diagnostics,omitempty"`
}

type SourceReadLease struct {
	ID         string
	HasSources bool
}

// SourceFileView is a single published, fixed-commit read of the complete file.
type SourceFileView struct {
	FileSize            int64                `json:"file_size"`
	DataSourceID        string               `json:"data_source_id"`
	KnowledgeBaseID     string               `json:"-"`
	KnowledgeID         string               `json:"knowledge_id"`
	SnapshotID          string               `json:"snapshot_id"`
	FileVersionID       string               `json:"file_version_id"`
	ProjectID           string               `json:"project_id"`
	CommitSHA           string               `json:"commit_sha"`
	RepositoryURL       string               `json:"repository_url"`
	Path                string               `json:"path"`
	SHA256              string               `json:"sha256"`
	Encoding            string               `json:"encoding"`
	Quality             string               `json:"quality"`
	ParserVersion       string               `json:"parser_version"`
	Content             string               `json:"content" gorm:"-"`
	RawContent          []byte               `json:"-"`
	Symbols             JSON                 `json:"symbols"`
	Facts               JSON                 `json:"facts"`
	Diagnostics         JSON                 `json:"diagnostics"`
	Relations           []SourceCodeRelation `json:"relations,omitempty" gorm:"-"`
	RelationsTruncated  bool                 `json:"relations_truncated" gorm:"-"`
	RelationsNextCursor string               `json:"relations_next_cursor,omitempty" gorm:"-"`
}

type SourceParsedArtifact struct {
	TenantID     uint64 `gorm:"primaryKey"`
	DataSourceID string `gorm:"type:varchar(36);primaryKey"`
	ArtifactKey  string `gorm:"type:text;primaryKey"`
	Parsed       JSON   `gorm:"type:jsonb"`
}
type SourceEmbeddingArtifact struct {
	TenantID     uint64 `gorm:"primaryKey"`
	DataSourceID string `gorm:"type:varchar(36);primaryKey"`
	ArtifactKey  string `gorm:"type:text;primaryKey"`
	Vector       JSON   `gorm:"type:jsonb"`
}
