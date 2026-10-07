package repository

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// LoadSourceWikiImpactSnapshot materializes every manifest row, parser fact,
// and relation for one exact source snapshot. Unlike the current-snapshot T15
// reader, this also supports the immediately previous retained publication.
// The caller supplies the logical stage; database identity and completeness
// are checked here and the pure planner validates the stage pairing.
func LoadSourceWikiImpactSnapshot(
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, snapshotID string,
	stage types.SourceWikiImpactSnapshotStage,
) (types.SourceWikiImpactSnapshot, *SourceWikiSkeletonSnapshot, error) {
	return loadSourceWikiImpactSnapshot(db, tenantID, knowledgeBaseID, sourceID, snapshotID, stage, nil)
}

// LoadSourceWikiImpactSnapshotWithRelationInventory reuses a previously
// bounded relation inventory and compares it to the fixed snapshot by streaming
// the database rows one at a time. This preserves exact inventory equality
// without retaining a second complete relation collection.
func LoadSourceWikiImpactSnapshotWithRelationInventory(
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, snapshotID string,
	stage types.SourceWikiImpactSnapshotStage,
	inventory *SourceWikiRelationInventory,
) (types.SourceWikiImpactSnapshot, *SourceWikiSkeletonSnapshot, error) {
	if inventory == nil || inventory.TenantID != tenantID || inventory.KnowledgeBaseID != knowledgeBaseID ||
		inventory.DataSourceID != sourceID || inventory.SnapshotID != snapshotID || !inventory.RelationsComplete ||
		inventory.ExpectedCount != len(inventory.Relations) || inventory.ExpectedCount > types.SourceWikiSkeletonMaxRelations {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonRelationInventoryChanged)
	}
	return loadSourceWikiImpactSnapshot(db, tenantID, knowledgeBaseID, sourceID, snapshotID, stage, inventory)
}

func loadSourceWikiImpactSnapshot(
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, snapshotID string,
	stage types.SourceWikiImpactSnapshotStage,
	relationInventory *SourceWikiRelationInventory,
) (types.SourceWikiImpactSnapshot, *SourceWikiSkeletonSnapshot, error) {
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("source Wiki impact read requires a fixed tenant, KB, source, and snapshot")
	}
	var snapshot types.SourceSnapshot
	if err := db.Where("id=? AND tenant_id=? AND knowledge_base_id=? AND data_source_id=? AND state='published'",
		snapshotID, tenantID, knowledgeBaseID, sourceID).Take(&snapshot).Error; err != nil {
		return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("source Wiki impact snapshot is unavailable: %w", err)
	}
	if !snapshot.ManifestComplete {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonManifestIncomplete)
	}
	if !snapshot.RelationsStaged {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationsIncomplete)
	}
	if snapshot.MemberCount < 0 {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonNegativeMemberCount)
	}
	if snapshot.RelationCount < 0 {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonNegativeRelationCount)
	}
	if snapshot.MemberCount > types.SourceWikiSkeletonMaxFiles {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonMemberCountExceeded)
	}
	if snapshot.RelationCount > types.SourceWikiSkeletonMaxRelations {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonRelationCountExceeded)
	}
	type memberInventorySize struct {
		MetadataBytes int64
		FactBytes     int64
		FactCount     int64
	}
	var memberInventory memberInventorySize
	if err := db.Table("source_snapshot_members sm").
		Joins("LEFT JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.snapshot_id=sm.snapshot_id").
		Joins("LEFT JOIN source_files sf ON sf.id=sm.source_file_id").
		Select(`COALESCE(SUM(
			OCTET_LENGTH(COALESCE(sm.path, '')) + OCTET_LENGTH(COALESCE(sm.source_file_id, '')) +
			OCTET_LENGTH(COALESCE(sm.file_version_id, '')) + OCTET_LENGTH(COALESCE(sm.blob_sha, '')) +
			OCTET_LENGTH(COALESCE(sm.status, '')) + OCTET_LENGTH(COALESCE(sf.id, '')) +
			OCTET_LENGTH(COALESCE(sf.knowledge_base_id, '')) + OCTET_LENGTH(COALESCE(sf.data_source_id, '')) +
			OCTET_LENGTH(COALESCE(sv.id, '')) + OCTET_LENGTH(COALESCE(sv.source_file_id, '')) +
			OCTET_LENGTH(COALESCE(sv.sha256, '')) + OCTET_LENGTH(COALESCE(sv.parser_version, '')) +
			OCTET_LENGTH(COALESCE(sv.quality, ''))
		), 0) AS metadata_bytes,
			COALESCE(SUM(OCTET_LENGTH(COALESCE(sv.facts::text, ''))), 0) AS fact_bytes,
			COALESCE(SUM(CASE WHEN sm.status='parsed' AND jsonb_typeof(sv.facts)='array' THEN jsonb_array_length(sv.facts) ELSE 0 END), 0) AS fact_count`).
		Where("sm.snapshot_id=?", snapshotID).Scan(&memberInventory).Error; err != nil {
		return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("read source Wiki impact member inventory bounds: %w", err)
	}
	if memberInventory.MetadataBytes > types.SourceWikiImpactMaxMemberMetadataBytes {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonMemberMetadataBytesExceeded)
	}
	if memberInventory.FactBytes > types.SourceWikiImpactMaxFactBytes {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactBytesExceeded)
	}
	if memberInventory.FactCount > types.SourceWikiImpactMaxFacts {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactCountExceeded)
	}
	var relationBounds struct {
		ContextBytes  int64
		RelationBytes int64
	}
	if err := db.Table("source_code_relations").Select(sourceWikiRelationBoundsSelect).
		Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).Scan(&relationBounds).Error; err != nil {
		return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("read source Wiki impact relation byte bounds: %w", err)
	}
	if relationBounds.ContextBytes > types.SourceWikiImpactMaxContextBytes {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonRelationBytesExceeded)
	}
	if relationBounds.RelationBytes > types.SourceWikiImpactMaxRelationBytes {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonRelationTotalBytesExceeded)
	}
	result := types.SourceWikiImpactSnapshot{
		TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, SourceID: sourceID, SnapshotID: snapshotID,
		Stage: stage, ManifestComplete: snapshot.ManifestComplete, ExpectedMemberCount: snapshot.MemberCount,
		RelationsComplete: snapshot.RelationsStaged, ExpectedRelationCount: snapshot.RelationCount,
	}
	type memberRow struct {
		Path                string
		SourceFileID        string
		FileVersionID       string
		BlobSHA             string
		Status              string
		Generated           bool
		FileID              string
		FileTenantID        uint64
		FileKBID            string
		FileSourceID        string
		VersionID           string
		VersionSourceFileID string
		ContentSHA          string
		ParserVersion       string
		Quality             string
		Facts               types.JSON
	}
	var rows []memberRow
	if err := db.Table("source_snapshot_members sm").
		Joins("LEFT JOIN source_files sf ON sf.id=sm.source_file_id").
		Joins("LEFT JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.snapshot_id=sm.snapshot_id").
		Select(`sm.path, sm.source_file_id, sm.file_version_id, sm.blob_sha, sm.status, sm.generated,
			sf.id AS file_id, sf.tenant_id AS file_tenant_id, sf.knowledge_base_id AS file_kb_id, sf.data_source_id AS file_source_id,
			sv.id AS version_id, sv.source_file_id AS version_source_file_id, sv.sha256 AS content_sha,
			sv.parser_version, sv.quality, sv.facts`).
		Where("sm.snapshot_id=?", snapshotID).Order("sm.path ASC").
		Limit(types.SourceWikiSkeletonMaxFiles + 1).Find(&rows).Error; err != nil {
		return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("load source Wiki impact members: %w", err)
	}
	if len(rows) > types.SourceWikiSkeletonMaxFiles {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonMemberCountExceeded)
	}
	if len(rows) != snapshot.MemberCount {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonMemberCountMismatch)
	}
	result.Members = make([]types.SourceWikiImpactMember, 0, len(rows))
	loaded := &SourceWikiSkeletonSnapshot{
		TenantID: tenantID, DataSourceID: sourceID, SnapshotID: snapshotID,
		Members: make([]SourceWikiSkeletonFactMember, 0, len(rows)),
		Files:   make([]types.SourceWikiSkeletonFile, 0, len(rows)),
	}
	seenPaths, seenFiles, seenVersions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	factCount, factBytes, materializedFactBytes, memberMetadataBytes := int64(0), int64(0), int64(0), int64(0)
	for _, row := range rows {
		memberMetadataBytes += sourceWikiMemberMetadataBytes(
			row.Path, row.SourceFileID, row.FileVersionID, row.BlobSHA, row.Status,
			row.FileID, row.FileKBID, row.FileSourceID, row.VersionID, row.VersionSourceFileID,
			row.ContentSHA, row.ParserVersion, row.Quality,
		)
		materializedFactBytes += int64(len(row.Facts))
		if memberMetadataBytes > types.SourceWikiImpactMaxMemberMetadataBytes {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonMemberMetadataBytesExceeded)
		}
		if materializedFactBytes > types.SourceWikiImpactMaxFactBytes {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactBytesExceeded)
		}
		// A joined file outside the requested scope is an authorization/identity
		// boundary failure, not a deterministic proof defect eligible for fallback.
		if row.FileID != "" && (row.FileTenantID != tenantID || row.FileKBID != knowledgeBaseID || row.FileSourceID != sourceID) {
			return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("source Wiki impact member file is outside the requested scope")
		}
		if row.Path == "" || seenPaths[row.Path] {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonDuplicateMemberPath)
		}
		seenPaths[row.Path] = true
		member := types.SourceWikiImpactMember{
			Path: row.Path, SourceFileID: row.SourceFileID, FileVersionID: row.FileVersionID,
			ContentSHA: row.ContentSHA, Status: row.Status, Generated: row.Generated,
			ParserVersion: row.ParserVersion, Quality: row.Quality,
		}
		if member.ContentSHA == "" {
			member.ContentSHA = row.BlobSHA
		}
		facts := []types.ParsedSourceFact{}
		switch row.Status {
		case "parsed":
			if row.SourceFileID == "" || row.FileVersionID == "" || row.FileID != row.SourceFileID ||
				row.VersionID != row.FileVersionID || row.VersionSourceFileID != row.SourceFileID ||
				row.ContentSHA == "" || row.ParserVersion == "" || row.Quality == "" {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonParsedMemberIdentity)
			}
			if seenFiles[row.SourceFileID] || seenVersions[row.FileVersionID] {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonDuplicateMemberIdentity)
			}
			seenFiles[row.SourceFileID], seenVersions[row.FileVersionID] = true, true
			trimmedFacts := bytes.TrimSpace(row.Facts)
			factBytes += int64(len(trimmedFacts))
			if factBytes > types.SourceWikiImpactMaxFactBytes {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactBytesExceeded)
			}
			if len(trimmedFacts) == 0 || trimmedFacts[0] != '[' || json.Unmarshal(trimmedFacts, &facts) != nil || facts == nil {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonParsedFactsInvalid)
			}
			factCount += int64(len(facts))
			if factCount > types.SourceWikiImpactMaxFacts {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactCountExceeded)
			}
			member.FactsComplete, member.ExpectedFactCount, member.Facts = true, len(facts), facts
			loaded.Members = append(loaded.Members, SourceWikiSkeletonFactMember{
				FileID: row.SourceFileID, VersionID: row.FileVersionID, Path: row.Path, Generated: row.Generated, Facts: facts,
			})
			loaded.Files = append(loaded.Files, types.SourceWikiSkeletonFile{Path: row.Path, Generated: row.Generated, Facts: facts})
		case "excluded":
			if row.FileVersionID != "" || row.VersionID != "" {
				return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
					SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonExcludedMemberVersion)
			}
			if row.SourceFileID != "" {
				if row.FileID != row.SourceFileID || seenFiles[row.SourceFileID] {
					return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
						SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonExcludedMemberIdentity)
				}
				seenFiles[row.SourceFileID] = true
			}
			member.FactsComplete, member.ExpectedFactCount = true, 0
		case "included":
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonMemberUnparsed)
		default:
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonUnsupportedMemberState)
		}
		result.Members = append(result.Members, member)
	}
	if memberMetadataBytes != memberInventory.MetadataBytes || materializedFactBytes != memberInventory.FactBytes {
		return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
			SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonMemberBytesMismatch)
	}
	loaded.Complete = true
	var relations []types.SourceCodeRelation
	if relationInventory != nil {
		relations = relationInventory.Relations
		if len(relations) != snapshot.RelationCount {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationCountMismatch)
		}
		if err := verifySourceWikiRelationInventoryRows(db, tenantID, sourceID, snapshotID, relations); err != nil {
			return types.SourceWikiImpactSnapshot{}, nil, err
		}
	} else {
		if err := db.Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).
			Order("from_path ASC, kind ASC, from_key ASC, to_path ASC, to_key ASC, id ASC").
			Limit(types.SourceWikiSkeletonMaxRelations + 1).Find(&relations).Error; err != nil {
			return types.SourceWikiImpactSnapshot{}, nil, fmt.Errorf("load source Wiki impact relations: %w", err)
		}
		if len(relations) > types.SourceWikiSkeletonMaxRelations {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonRelationCountExceeded)
		}
		if len(relations) != snapshot.RelationCount {
			return types.SourceWikiImpactSnapshot{}, nil, newSourceWikiImpactLoadError(
				SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationCountMismatch)
		}
	}
	result.Relations = relations
	loaded.Relations = relations
	return result, loaded, nil
}

func sourceWikiMemberMetadataBytes(values ...string) int64 {
	var total int64
	for _, value := range values {
		total += int64(len(value))
	}
	return total
}

func verifySourceWikiRelationInventoryRows(
	db *gorm.DB,
	tenantID uint64,
	sourceID, snapshotID string,
	expected []types.SourceCodeRelation,
) error {
	rows, err := db.Model(&types.SourceCodeRelation{}).
		Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).
		Order("from_path ASC, kind ASC, from_key ASC, to_path ASC, to_key ASC, id ASC").Rows()
	if err != nil {
		return fmt.Errorf("stream source Wiki impact relations for inventory equality: %w", err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(expected) {
			return newSourceWikiImpactLoadError(SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationInventoryChanged)
		}
		var actual types.SourceCodeRelation
		if err := db.ScanRows(rows, &actual); err != nil {
			return fmt.Errorf("scan source Wiki impact relation for inventory equality: %w", err)
		}
		if !sourceWikiRelationEqual(actual, expected[index]) {
			return newSourceWikiImpactLoadError(SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationInventoryChanged)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("stream source Wiki impact relations for inventory equality: %w", err)
	}
	if index != len(expected) {
		return newSourceWikiImpactLoadError(SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationInventoryChanged)
	}
	return nil
}

func sourceWikiRelationEqual(left, right types.SourceCodeRelation) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID && left.DataSourceID == right.DataSourceID &&
		left.SnapshotID == right.SnapshotID && left.Kind == right.Kind && left.FromFileID == right.FromFileID &&
		left.FromVersionID == right.FromVersionID && left.FromPath == right.FromPath && left.FromKey == right.FromKey &&
		bytes.Equal(left.FromRange, right.FromRange) && left.ToFileID == right.ToFileID &&
		left.ToVersionID == right.ToVersionID && left.ToPath == right.ToPath && left.ToKey == right.ToKey &&
		bytes.Equal(left.ToRange, right.ToRange) && left.Determinacy == right.Determinacy &&
		left.Quality == right.Quality && left.ResolutionReason == right.ResolutionReason &&
		bytes.Equal(left.Context, right.Context) && left.CreatedAt.Equal(right.CreatedAt)
}
