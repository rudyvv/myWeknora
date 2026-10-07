package repository

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const (
	sourceWikiModuleProjectionPageSize          = 128
	sourceWikiModuleProjectionMaxPathBytes      = 4096
	sourceWikiModuleProjectionMaxTotalPathBytes = 8 << 20
	sourceWikiModuleProjectionMaxSeedStringSize = 16 << 10
	sourceWikiModuleProjectionMaxSeedBytes      = types.SourceWikiImpactMaxFactBytes
)

// ErrSourceWikiModuleProjectionBudgetExceeded marks a deterministic capacity
// limit that should defer Wiki generation without affecting source search.
var ErrSourceWikiModuleProjectionBudgetExceeded = errors.New("source Wiki module projection budget exceeded")

// SourceWikiModuleProjection is a bounded projection of one fixed current
// publication. Paths are limited to 4096 UTF-8 bytes each and 8 MiB total.
// InventoryComplete proves that all manifest rows were paged and their
// identities checked; it does not prove the manifest digest was reconstructed
// from those rows. Relations are intentionally deferred because this
// projection does not load or derive a complete relation graph.
type SourceWikiModuleProjection struct {
	TenantID          uint64
	KnowledgeBaseID   string
	DataSourceID      string
	SnapshotID        string
	ManifestDigest    string // Persisted fixed-manifest identifier; not recomputed here.
	MemberCount       int
	ParsedFileCount   int
	InventoryComplete bool
	RelationsComplete bool
	RelationsDeferred bool
	Members           []SourceWikiModuleProjectionMember
}

// SourceWikiModuleProjectionMember carries the manifest identity for one
// parsed or excluded path and, for non-generated parsed files only, its single
// highest-priority structural module seed.
type SourceWikiModuleProjectionMember struct {
	Path          string
	SourceFileID  string
	FileVersionID string
	BlobSHA       string
	ContentSHA256 string // Persisted version identity; source bytes are verified by the evidence collector.
	Status        string
	Generated     bool
	Seed          *SourceWikiModuleSeed
}

// SourceWikiModuleSeed contains only the three parser strings used by module
// planning, their source-array ordinal, and the shared classifier priority.
type SourceWikiModuleSeed struct {
	Kind     string
	Name     string
	Quality  string
	Ordinal  int64
	Priority int
}

type sourceWikiModuleProjectionSnapshotRow struct {
	ID               string
	TenantID         uint64
	KnowledgeBaseID  string
	DataSourceID     string
	ManifestComplete bool
	ManifestDigest   string
	MemberCount      int
	FileCount        int
}

type sourceWikiModuleProjectionBounds struct {
	MemberCount                  int64
	DistinctPathCount            int64
	EmptyPathCount               int64
	MaxPathBytes                 int64
	TotalPathBytes               int64
	FactCount                    int64
	FactBytes                    int64
	ParsedCount                  int64
	ExcludedCount                int64
	SupportedStatusCount         int64
	FileIdentityCount            int64
	DistinctFileIdentityCount    int64
	VersionIdentityCount         int64
	DistinctVersionIdentityCount int64
	ValidBlobCount               int64
	ValidParsedIdentityCount     int64
	ValidExcludedIdentityCount   int64
}

type sourceWikiModuleProjectionMemberRow struct {
	Path                string
	SourceFileID        string
	FileVersionID       string
	BlobSHA             string
	Status              string
	Generated           bool
	FileID              string
	FileTenantID        *uint64
	FileKnowledgeBaseID string
	FileDataSourceID    string
	FilePath            string
	VersionID           string
	VersionSourceFileID string
	VersionSnapshotID   string
	VersionBlobSHA      string
	ContentSHA256       string
	SeedKind            *string
	SeedName            *string
	SeedQuality         *string
	SeedOrdinal         *int64
	SeedPriority        *int64
	SeedOversized       *bool
}

// LoadSourceWikiModuleProjection pages every member of one fixed current
// publication, including excluded members. Each page returns metadata and at
// most one structural seed per non-generated parsed file; parser facts,
// source content, symbols, and relation context are never materialized in Go.
// Path bytes are bounded at 4096 per path and 8 MiB in aggregate; any budget
// violation fails closed without returning a partial projection. Candidate
// facts use the existing impact inventory count/byte limits before seed paging.
func LoadSourceWikiModuleProjection(
	ctx context.Context,
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, snapshotID string,
) (*SourceWikiModuleProjection, error) {
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, fmt.Errorf("source Wiki module projection requires a fixed tenant, KB, source, and snapshot")
	}
	if db.Statement != nil {
		if committer, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok && committer != nil {
			return nil, fmt.Errorf("source Wiki module projection does not accept an existing transaction")
		}
	}
	if db.Dialector.Name() != "postgres" {
		return nil, fmt.Errorf("source Wiki module projection requires PostgreSQL")
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, fmt.Errorf("validate source Wiki module projection read scope: %w", err)
	}

	var projection *SourceWikiModuleProjection
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot sourceWikiModuleProjectionSnapshotRow
		query := tx.Table("source_snapshots ss").
			Select(`ss.id, ss.tenant_id, ss.knowledge_base_id, ss.data_source_id,
				ss.manifest_complete, ss.manifest_digest, ss.member_count, ss.file_count`).
			Joins(`JOIN source_publications sp ON sp.snapshot_id=ss.id AND sp.data_source_id=ss.data_source_id
				AND sp.tenant_id=ss.tenant_id AND sp.knowledge_base_id=ss.knowledge_base_id`).
			Where(`ss.id=? AND ss.tenant_id=? AND ss.knowledge_base_id=? AND ss.data_source_id=?
				AND ss.state='published' AND ss.manifest_complete=true`, snapshotID, tenantID, knowledgeBaseID, sourceID).
			Where(source.SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "NULL")).
			Where(source.SourcePermissionSQL(ctx, "ss.data_source_id", "NULL"))
		if err := query.Take(&snapshot).Error; err != nil {
			return fmt.Errorf("source Wiki module projection publication is unavailable or unauthorized: %w", err)
		}
		if snapshot.ID != snapshotID || snapshot.TenantID != tenantID || snapshot.KnowledgeBaseID != knowledgeBaseID || snapshot.DataSourceID != sourceID {
			return fmt.Errorf("source Wiki module projection publication identity changed")
		}
		if !snapshot.ManifestComplete || snapshot.MemberCount < 0 || snapshot.FileCount < 0 || snapshot.FileCount > snapshot.MemberCount {
			return fmt.Errorf("source Wiki module projection manifest counts are invalid")
		}
		if snapshot.MemberCount > types.SourceWikiSkeletonMaxFiles {
			return fmt.Errorf("%w: manifest exceeds the %d-member inventory bound", ErrSourceWikiModuleProjectionBudgetExceeded, types.SourceWikiSkeletonMaxFiles)
		}
		if len(snapshot.ManifestDigest) != 64 {
			return fmt.Errorf("source Wiki module projection manifest digest identifier is malformed")
		}
		if _, err := hex.DecodeString(snapshot.ManifestDigest); err != nil || strings.ToLower(snapshot.ManifestDigest) != snapshot.ManifestDigest {
			return fmt.Errorf("source Wiki module projection manifest digest identifier is malformed")
		}

		var bounds sourceWikiModuleProjectionBounds
		if err := tx.Raw(sourceWikiModuleProjectionBoundsSQL, tenantID, knowledgeBaseID, sourceID, snapshotID).Scan(&bounds).Error; err != nil {
			return fmt.Errorf("validate source Wiki module projection manifest inventory: %w", err)
		}
		if bounds.MaxPathBytes > sourceWikiModuleProjectionMaxPathBytes {
			return fmt.Errorf("%w: path exceeds the %d-byte hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxPathBytes)
		}
		if bounds.TotalPathBytes > sourceWikiModuleProjectionMaxTotalPathBytes {
			return fmt.Errorf("%w: paths exceed the %d-byte aggregate hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxTotalPathBytes)
		}
		if bounds.FactCount > types.SourceWikiImpactMaxFacts {
			return fmt.Errorf("%w: candidate facts exceed the %d-fact hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, types.SourceWikiImpactMaxFacts)
		}
		if bounds.FactBytes > types.SourceWikiImpactMaxFactBytes {
			return fmt.Errorf("%w: candidate facts exceed the %d-byte hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, types.SourceWikiImpactMaxFactBytes)
		}
		if bounds.MemberCount != int64(snapshot.MemberCount) || bounds.DistinctPathCount != bounds.MemberCount || bounds.EmptyPathCount != 0 ||
			bounds.ParsedCount != int64(snapshot.FileCount) || bounds.SupportedStatusCount != bounds.MemberCount ||
			bounds.FileIdentityCount != bounds.DistinctFileIdentityCount || bounds.VersionIdentityCount != bounds.DistinctVersionIdentityCount ||
			bounds.ValidBlobCount != bounds.MemberCount || bounds.ValidParsedIdentityCount != bounds.ParsedCount ||
			bounds.ValidExcludedIdentityCount != bounds.ExcludedCount || bounds.ParsedCount+bounds.ExcludedCount != bounds.MemberCount {
			return fmt.Errorf("source Wiki module projection manifest inventory is incomplete or has invalid identities")
		}

		projection = &SourceWikiModuleProjection{
			TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, DataSourceID: sourceID, SnapshotID: snapshotID,
			ManifestDigest: snapshot.ManifestDigest, MemberCount: snapshot.MemberCount, ParsedFileCount: snapshot.FileCount,
			RelationsComplete: false, RelationsDeferred: true,
			Members: make([]SourceWikiModuleProjectionMember, 0, snapshot.MemberCount),
		}
		seenPaths := make(map[string]struct{}, snapshot.MemberCount)
		seenFiles := make(map[string]struct{}, snapshot.MemberCount)
		seenVersions := make(map[string]struct{}, snapshot.FileCount)
		lastPath := ""
		var seedBytes, pathBytes int64
		for len(projection.Members) < snapshot.MemberCount {
			var rows []sourceWikiModuleProjectionMemberRow
			if err := tx.Raw(sourceWikiModuleProjectionPageSQL, sourceWikiModuleProjectionMaxSeedStringSize, snapshotID, lastPath).Scan(&rows).Error; err != nil {
				return fmt.Errorf("read source Wiki module projection page: %w", err)
			}
			if len(rows) == 0 || len(rows) > sourceWikiModuleProjectionPageSize || len(projection.Members)+len(rows) > snapshot.MemberCount {
				return fmt.Errorf("source Wiki module projection pagination did not cover the fixed manifest")
			}
			for _, row := range rows {
				if row.SeedOversized != nil && *row.SeedOversized {
					return fmt.Errorf("%w: selected seed exceeds the %d-byte per-field hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxSeedStringSize)
				}
				nextPathBytes, pathErr := addSourceWikiModuleProjectionPathBytes(pathBytes, row.Path)
				if pathErr != nil {
					return pathErr
				}
				pathBytes = nextPathBytes
				if row.Path == "" || row.Path == lastPath || row.BlobSHA == "" {
					return fmt.Errorf("source Wiki module projection contains an empty or unordered manifest path")
				}
				if _, exists := seenPaths[row.Path]; exists {
					return fmt.Errorf("source Wiki module projection contains a duplicate manifest path")
				}
				seenPaths[row.Path] = struct{}{}
				lastPath = row.Path
				member := SourceWikiModuleProjectionMember{
					Path: row.Path, SourceFileID: row.SourceFileID, FileVersionID: row.FileVersionID,
					BlobSHA: row.BlobSHA, Status: row.Status, Generated: row.Generated,
				}
				switch row.Status {
				case "parsed":
					if row.SourceFileID == "" || row.FileVersionID == "" || row.FileID != row.SourceFileID ||
						row.FileTenantID == nil || *row.FileTenantID != tenantID || row.FileKnowledgeBaseID != knowledgeBaseID ||
						row.FileDataSourceID != sourceID || row.FilePath != row.Path || row.VersionID != row.FileVersionID ||
						row.VersionSourceFileID != row.SourceFileID || row.VersionSnapshotID != snapshotID ||
						row.VersionBlobSHA != row.BlobSHA || !isSourceSHA256(row.ContentSHA256) {
						return fmt.Errorf("source Wiki module projection parsed member identity is invalid")
					}
					if _, exists := seenFiles[row.SourceFileID]; exists {
						return fmt.Errorf("source Wiki module projection contains a duplicate file identity")
					}
					if _, exists := seenVersions[row.FileVersionID]; exists {
						return fmt.Errorf("source Wiki module projection contains a duplicate version identity")
					}
					seenFiles[row.SourceFileID], seenVersions[row.FileVersionID] = struct{}{}, struct{}{}
					member.ContentSHA256 = row.ContentSHA256
					if row.SeedPriority != nil || row.SeedOrdinal != nil || row.SeedKind != nil || row.SeedName != nil || row.SeedQuality != nil {
						if row.Generated || row.SeedPriority == nil || row.SeedOrdinal == nil || row.SeedKind == nil || row.SeedName == nil || row.SeedQuality == nil || *row.SeedOrdinal < 1 {
							return fmt.Errorf("source Wiki module projection seed identity is incomplete")
						}
						if len(*row.SeedKind) > sourceWikiModuleProjectionMaxSeedStringSize ||
							len(*row.SeedName) > sourceWikiModuleProjectionMaxSeedStringSize ||
							len(*row.SeedQuality) > sourceWikiModuleProjectionMaxSeedStringSize {
							return fmt.Errorf("%w: seed string exceeds its hard bound", ErrSourceWikiModuleProjectionBudgetExceeded)
						}
						seed := types.ParsedSourceFact{Kind: *row.SeedKind, Name: *row.SeedName, Quality: *row.SeedQuality}
						priority := source.SourceWikiModuleFactPriority(seed)
						if priority == 0 || int64(priority) != *row.SeedPriority {
							return fmt.Errorf("source Wiki module projection SQL seed priority disagrees with the shared selector")
						}
						seedBytes += int64(len(*row.SeedKind) + len(*row.SeedName) + len(*row.SeedQuality))
						if seedBytes > sourceWikiModuleProjectionMaxSeedBytes {
							return fmt.Errorf("%w: seed inventory exceeds the %d-byte hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxSeedBytes)
						}
						member.Seed = &SourceWikiModuleSeed{
							Kind: *row.SeedKind, Name: *row.SeedName, Quality: *row.SeedQuality,
							Ordinal: *row.SeedOrdinal, Priority: priority,
						}
					}
				case "excluded":
					if row.FileVersionID != "" || row.VersionID != "" || row.SeedPriority != nil || row.SeedOrdinal != nil || row.SeedKind != nil ||
						row.SeedName != nil || row.SeedQuality != nil || row.SourceFileID != row.FileID && row.SourceFileID != "" ||
						row.FileID != "" && (row.FileTenantID == nil || *row.FileTenantID != tenantID || row.FileKnowledgeBaseID != knowledgeBaseID ||
							row.FileDataSourceID != sourceID || row.FilePath != row.Path) {
						return fmt.Errorf("source Wiki module projection excluded member identity is invalid")
					}
					if row.SourceFileID != "" {
						if _, exists := seenFiles[row.SourceFileID]; exists {
							return fmt.Errorf("source Wiki module projection contains a duplicate file identity")
						}
						seenFiles[row.SourceFileID] = struct{}{}
					}
				default:
					return fmt.Errorf("source Wiki module projection contains an unsupported manifest status")
				}
				projection.Members = append(projection.Members, member)
			}
		}
		if len(projection.Members) != snapshot.MemberCount || int64(len(seenVersions)) != bounds.ParsedCount || pathBytes != bounds.TotalPathBytes {
			return fmt.Errorf("source Wiki module projection did not cover every manifest identity")
		}
		projection.InventoryComplete = true
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, fmt.Errorf("source Wiki module projection read scope changed: %w", err)
	}
	return projection, nil
}

const sourceWikiModuleProjectionBoundsSQL = `
WITH requested AS (
	SELECT ?::bigint AS tenant_id, ?::varchar(36) AS knowledge_base_id,
		?::varchar(36) AS data_source_id, ?::varchar(36) AS snapshot_id
)
SELECT
	COUNT(*) AS member_count,
	COUNT(DISTINCT sm.path) AS distinct_path_count,
	COUNT(*) FILTER (WHERE sm.path IS NULL OR sm.path='') AS empty_path_count,
	COALESCE(MAX(OCTET_LENGTH(sm.path)),0) AS max_path_bytes,
	COALESCE(SUM(OCTET_LENGTH(sm.path)),0) AS total_path_bytes,
	COALESCE(SUM(CASE WHEN sm.status='parsed' AND sm.generated=false AND jsonb_typeof(sv.facts)='array'
		THEN jsonb_array_length(sv.facts) ELSE 0 END),0) AS fact_count,
	COALESCE(SUM(CASE WHEN sm.status='parsed' AND sm.generated=false AND jsonb_typeof(sv.facts)='array'
		THEN OCTET_LENGTH(sv.facts::text) ELSE 0 END),0) AS fact_bytes,
	COUNT(*) FILTER (WHERE sm.status='parsed') AS parsed_count,
	COUNT(*) FILTER (WHERE sm.status='excluded') AS excluded_count,
	COUNT(*) FILTER (WHERE sm.status IN ('parsed','excluded')) AS supported_status_count,
	COUNT(*) FILTER (WHERE NULLIF(sm.source_file_id,'') IS NOT NULL) AS file_identity_count,
	COUNT(DISTINCT NULLIF(sm.source_file_id,'')) AS distinct_file_identity_count,
	COUNT(*) FILTER (WHERE NULLIF(sm.file_version_id,'') IS NOT NULL) AS version_identity_count,
	COUNT(DISTINCT NULLIF(sm.file_version_id,'')) AS distinct_version_identity_count,
	COUNT(*) FILTER (WHERE sm.blob_sha ~* '^([0-9a-f]{40}|[0-9a-f]{64})$') AS valid_blob_count,
	COUNT(*) FILTER (WHERE sm.status='parsed' AND
		sm.source_file_id<>'' AND sm.file_version_id<>'' AND
		sf.id=sm.source_file_id AND sf.tenant_id=requested.tenant_id AND
		sf.knowledge_base_id=requested.knowledge_base_id AND sf.data_source_id=requested.data_source_id AND sf.path=sm.path AND
		sv.id=sm.file_version_id AND sv.source_file_id=sm.source_file_id AND sv.snapshot_id=requested.snapshot_id AND
		sv.blob_sha=sm.blob_sha AND sv.sha256 ~* '^[0-9a-f]{64}$' AND
		NULLIF(sv.parser_version,'') IS NOT NULL AND NULLIF(sv.quality,'') IS NOT NULL AND
		jsonb_typeof(sv.facts)='array') AS valid_parsed_identity_count,
	COUNT(*) FILTER (WHERE sm.status='excluded' AND sm.file_version_id='' AND
		(sm.source_file_id='' OR (sf.id=sm.source_file_id AND sf.tenant_id=requested.tenant_id AND
		 sf.knowledge_base_id=requested.knowledge_base_id AND sf.data_source_id=requested.data_source_id AND sf.path=sm.path)))
		AS valid_excluded_identity_count
FROM source_snapshot_members sm
CROSS JOIN requested
LEFT JOIN source_files sf ON sf.id=NULLIF(sm.source_file_id,'')
LEFT JOIN source_file_versions sv ON sv.id=NULLIF(sm.file_version_id,'')
WHERE sm.snapshot_id=requested.snapshot_id`

const sourceWikiModuleProjectionPageSQL = `
WITH projection_limits AS (
	SELECT ?::bigint AS max_seed_string_bytes
)
SELECT sm.path, sm.source_file_id, sm.file_version_id, sm.blob_sha, sm.status, sm.generated,
	sf.id AS file_id, sf.tenant_id AS file_tenant_id, sf.knowledge_base_id AS file_knowledge_base_id,
	sf.data_source_id AS file_data_source_id, sf.path AS file_path,
	sv.id AS version_id, sv.source_file_id AS version_source_file_id, sv.snapshot_id AS version_snapshot_id,
	sv.blob_sha AS version_blob_sha, sv.sha256 AS content_sha256,
	CASE WHEN seed.oversized THEN NULL ELSE seed.kind END AS seed_kind,
	CASE WHEN seed.oversized THEN NULL ELSE seed.name END AS seed_name,
	CASE WHEN seed.oversized THEN NULL ELSE seed.quality END AS seed_quality,
	CASE WHEN seed.oversized THEN NULL ELSE seed.ordinal END AS seed_ordinal,
	CASE WHEN seed.oversized THEN NULL ELSE seed.priority END AS seed_priority,
	seed.oversized AS seed_oversized
FROM (
	SELECT source_member.*
	FROM source_snapshot_members source_member
	WHERE source_member.snapshot_id=? AND source_member.path COLLATE "C" > ? COLLATE "C"
	ORDER BY source_member.path COLLATE "C" ASC
	LIMIT 128
) sm
CROSS JOIN projection_limits
LEFT JOIN source_files sf ON sf.id=NULLIF(sm.source_file_id,'')
LEFT JOIN source_file_versions sv ON sv.id=NULLIF(sm.file_version_id,'')
LEFT JOIN LATERAL (
	SELECT candidate.kind, candidate.name, candidate.quality, candidate.ordinal, candidate.priority,
		COALESCE(OCTET_LENGTH(candidate.kind),0)>projection_limits.max_seed_string_bytes OR
		COALESCE(OCTET_LENGTH(candidate.name),0)>projection_limits.max_seed_string_bytes OR
		COALESCE(OCTET_LENGTH(candidate.quality),0)>projection_limits.max_seed_string_bytes AS oversized
	FROM (
		SELECT parsed_fact.value->>'kind' AS kind,
			COALESCE(parsed_fact.value->>'name','') AS name,
			COALESCE(parsed_fact.value->>'quality','') AS quality,
			parsed_fact.ordinality AS ordinal,
			CASE
				WHEN COALESCE(parsed_fact.value->>'quality','') NOT IN ('','structural') THEN 0
				WHEN parsed_fact.value->>'kind'='spring_mapping' THEN 110
				WHEN parsed_fact.value->>'kind'='java_type' AND (
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('controller'))='controller' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('service'))='service' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('serviceimpl'))='serviceimpl' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('mapper'))='mapper' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('repository'))='repository' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('configuration'))='configuration' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('config'))='config' OR
					RIGHT(LOWER(COALESCE(parsed_fact.value->>'name','')),CHAR_LENGTH('application'))='application'
				) THEN 90
				WHEN parsed_fact.value->>'kind' IN ('mybatis_mapper','mybatis_statement','mybatis_result_map') THEN 75
				ELSE 0
			END AS priority
		FROM jsonb_array_elements(CASE
			WHEN sm.status='parsed' AND sm.generated=false AND jsonb_typeof(sv.facts)='array' THEN sv.facts
			ELSE '[]'::jsonb END)
			WITH ORDINALITY AS parsed_fact(value, ordinality)
	) candidate
	WHERE candidate.priority>0
	ORDER BY candidate.priority DESC, candidate.ordinal ASC
	LIMIT 1
) seed ON TRUE
ORDER BY sm.path COLLATE "C" ASC
LIMIT 128`

func isSourceSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func addSourceWikiModuleProjectionPathBytes(total int64, path string) (int64, error) {
	if total < 0 || total > sourceWikiModuleProjectionMaxTotalPathBytes {
		return total, fmt.Errorf("source Wiki module projection path byte total is outside its hard bound")
	}
	pathBytes := int64(len(path))
	if pathBytes == 0 {
		return total, fmt.Errorf("source Wiki module projection contains an empty manifest path")
	}
	if pathBytes > sourceWikiModuleProjectionMaxPathBytes {
		return total, fmt.Errorf("%w: path exceeds the %d-byte hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxPathBytes)
	}
	if pathBytes > sourceWikiModuleProjectionMaxTotalPathBytes-total {
		return total, fmt.Errorf("%w: paths exceed the %d-byte aggregate hard bound", ErrSourceWikiModuleProjectionBudgetExceeded, sourceWikiModuleProjectionMaxTotalPathBytes)
	}
	return total + pathBytes, nil
}
