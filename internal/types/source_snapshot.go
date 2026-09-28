package types

import "time"

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

type SourceSymbol struct {
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	QualifiedName  string          `json:"qualified_name"`
	Signature      string          `json:"signature"`
	SignatureRange SourceRange     `json:"signature_range"`
	Range          SourceRange     `json:"range"`
	Annotations    []SourceContext `json:"annotations"`
}

type ParsedSourceChunk struct {
	Content string          `json:"content"`
	Range   SourceRange     `json:"range"`
	Quality string          `json:"quality"`
	Symbols []string        `json:"symbols"`
	Context []SourceContext `json:"context"`
}

type ParsedSourceFile struct {
	ParserVersion string              `json:"parser_version"`
	SHA256        string              `json:"sha256"`
	ByteLength    int                 `json:"byte_length"`
	Encoding      string              `json:"encoding"`
	Quality       string              `json:"quality"`
	Symbols       []SourceSymbol      `json:"symbols"`
	Chunks        []ParsedSourceChunk `json:"chunks"`
}

// SourceFile's ID is also its stable KnowledgeID. Repository text is versioned
// independently of user-managed Knowledge tags and descriptions.
type SourceFile struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	DataSourceID    string `json:"data_source_id" gorm:"type:varchar(36);uniqueIndex:source_file_path"`
	Path            string `json:"path" gorm:"type:text;uniqueIndex:source_file_path"`
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
	CreatedAt     time.Time `json:"created_at"`
}

type SourceSnapshot struct {
	ID               string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID         uint64     `json:"tenant_id"`
	KnowledgeBaseID  string     `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	DataSourceID     string     `json:"data_source_id" gorm:"type:varchar(36);index"`
	SyncLogID        string     `json:"sync_log_id" gorm:"type:varchar(36);uniqueIndex"`
	ProjectID        string     `json:"project_id"`
	CommitSHA        string     `json:"commit_sha"`
	RepositoryURL    string     `json:"repository_url"`
	RulesVersion     string     `json:"rules_version"`
	State            string     `json:"state"`
	ManifestComplete bool       `json:"manifest_complete"`
	ManifestDigest   string     `json:"manifest_digest"`
	MemberCount      int        `json:"member_count"`
	FileCount        int        `json:"file_count"`
	ChunkCount       int        `json:"chunk_count"`
	Error            string     `json:"error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	PublishedAt      *time.Time `json:"published_at,omitempty"`
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

type SourceEvidence struct {
	SnapshotID    string          `json:"snapshot_id"`
	FileVersionID string          `json:"file_version_id"`
	ProjectID     string          `json:"project_id"`
	CommitSHA     string          `json:"commit_sha"`
	Path          string          `json:"path"`
	Range         SourceRange     `json:"range"`
	Symbols       []string        `json:"symbols"`
	Quality       string          `json:"quality"`
	GitLabURL     string          `json:"gitlab_url"`
	Context       []SourceContext `json:"context"`
}

// SourceFileView is a single published, fixed-commit read of the complete file.
type SourceFileView struct {
	KnowledgeID   string `json:"knowledge_id"`
	SnapshotID    string `json:"snapshot_id"`
	FileVersionID string `json:"file_version_id"`
	ProjectID     string `json:"project_id"`
	CommitSHA     string `json:"commit_sha"`
	RepositoryURL string `json:"repository_url"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Encoding      string `json:"encoding"`
	Quality       string `json:"quality"`
	ParserVersion string `json:"parser_version"`
	Content       string `json:"content" gorm:"-"`
	RawContent    []byte `json:"-"`
	Symbols       JSON   `json:"symbols"`
}
