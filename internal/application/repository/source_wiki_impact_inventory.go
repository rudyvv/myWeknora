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
	var result types.SourceWikiImpactSnapshot
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return result, nil, fmt.Errorf("source Wiki impact read requires a fixed tenant, KB, source, and snapshot")
	}
	var snapshot types.SourceSnapshot
	if err := db.Where("id=? AND tenant_id=? AND knowledge_base_id=? AND data_source_id=? AND state='published'",
		snapshotID, tenantID, knowledgeBaseID, sourceID).Take(&snapshot).Error; err != nil {
		return result, nil, fmt.Errorf("source Wiki impact snapshot is unavailable: %w", err)
	}
	result = types.SourceWikiImpactSnapshot{
		TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, SourceID: sourceID, SnapshotID: snapshotID,
		Stage: stage, ManifestComplete: snapshot.ManifestComplete, ExpectedMemberCount: snapshot.MemberCount,
		RelationsComplete: snapshot.RelationsStaged, ExpectedRelationCount: snapshot.RelationCount,
	}
	var factBytes int64
	if err := db.Table("source_snapshot_members sm").
		Joins("LEFT JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.snapshot_id=sm.snapshot_id").
		Select("COALESCE(SUM(OCTET_LENGTH(sv.facts::text)), 0)").
		Where("sm.snapshot_id=? AND sm.status='parsed'", snapshotID).Scan(&factBytes).Error; err != nil {
		return result, nil, err
	}
	if factBytes > types.SourceWikiImpactMaxFactBytes {
		return result, nil, fmt.Errorf("source Wiki impact fact inventory exceeds %d bytes", types.SourceWikiImpactMaxFactBytes)
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
		return result, nil, err
	}
	if len(rows) > types.SourceWikiSkeletonMaxFiles {
		return result, nil, fmt.Errorf("source Wiki impact member inventory exceeds %d", types.SourceWikiSkeletonMaxFiles)
	}
	result.Members = make([]types.SourceWikiImpactMember, 0, len(rows))
	loaded := &SourceWikiSkeletonSnapshot{
		TenantID: tenantID, DataSourceID: sourceID, SnapshotID: snapshotID,
		Members: make([]SourceWikiSkeletonFactMember, 0, len(rows)),
		Files:   make([]types.SourceWikiSkeletonFile, 0, len(rows)),
	}
	seenPaths, seenFiles, seenVersions := map[string]bool{}, map[string]bool{}, map[string]bool{}
	factCount, factBytes := 0, 0
	for _, row := range rows {
		if row.Path == "" || seenPaths[row.Path] {
			return result, nil, fmt.Errorf("source Wiki impact snapshot contains an empty or duplicate path")
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
				row.FileTenantID != tenantID || row.FileKBID != knowledgeBaseID || row.FileSourceID != sourceID ||
				row.VersionID != row.FileVersionID || row.VersionSourceFileID != row.SourceFileID ||
				row.ContentSHA == "" || row.ParserVersion == "" || row.Quality == "" {
				return result, nil, fmt.Errorf("parsed source Wiki impact member %q has incomplete immutable identity", row.Path)
			}
			if seenFiles[row.SourceFileID] || seenVersions[row.FileVersionID] {
				return result, nil, fmt.Errorf("source Wiki impact snapshot has duplicate file identities")
			}
			seenFiles[row.SourceFileID], seenVersions[row.FileVersionID] = true, true
			trimmedFacts := bytes.TrimSpace(row.Facts)
			factBytes += int64(len(trimmedFacts))
			if factBytes > types.SourceWikiImpactMaxFactBytes {
				return result, nil, fmt.Errorf("source Wiki impact fact inventory exceeds %d bytes", types.SourceWikiImpactMaxFactBytes)
			}
			if len(trimmedFacts) == 0 || trimmedFacts[0] != '[' || json.Unmarshal(trimmedFacts, &facts) != nil || facts == nil {
				return result, nil, fmt.Errorf("parser facts for source Wiki impact member %q are missing or invalid", row.Path)
			}
			factCount += len(facts)
			if factCount > types.SourceWikiImpactMaxFacts {
				return result, nil, fmt.Errorf("source Wiki impact fact inventory exceeds %d facts", types.SourceWikiImpactMaxFacts)
			}
			member.FactsComplete, member.ExpectedFactCount, member.Facts = true, len(facts), facts
			loaded.Members = append(loaded.Members, SourceWikiSkeletonFactMember{
				FileID: row.SourceFileID, VersionID: row.FileVersionID, Path: row.Path, Generated: row.Generated, Facts: facts,
			})
			loaded.Files = append(loaded.Files, types.SourceWikiSkeletonFile{Path: row.Path, Generated: row.Generated, Facts: facts})
		case "excluded":
			if row.FileVersionID != "" || row.VersionID != "" {
				return result, nil, fmt.Errorf("excluded source Wiki impact member %q unexpectedly has a parsed version", row.Path)
			}
			if row.SourceFileID != "" {
				if row.FileID != row.SourceFileID || row.FileTenantID != tenantID || row.FileKBID != knowledgeBaseID || row.FileSourceID != sourceID || seenFiles[row.SourceFileID] {
					return result, nil, fmt.Errorf("excluded source Wiki impact member %q has an invalid file identity", row.Path)
				}
				seenFiles[row.SourceFileID] = true
			}
			member.FactsComplete, member.ExpectedFactCount = true, 0
		case "included":
			// A complete source publication should not contain a merely selected,
			// unparsed entry. The planner sees this status and fails closed.
			member.FactsComplete = false
		default:
			return result, nil, fmt.Errorf("unsupported source Wiki impact member state %q", row.Status)
		}
		result.Members = append(result.Members, member)
	}
	loaded.Complete = snapshot.ManifestComplete && len(rows) == snapshot.MemberCount
	var contextBytes int64
	if err := db.Table("source_code_relations").Select("COALESCE(SUM(OCTET_LENGTH(context::text)), 0)").
		Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).Scan(&contextBytes).Error; err != nil {
		return result, nil, err
	}
	if contextBytes > types.SourceWikiImpactMaxContextBytes {
		return result, nil, fmt.Errorf("source Wiki impact relation context exceeds %d bytes", types.SourceWikiImpactMaxContextBytes)
	}
	var relations []types.SourceCodeRelation
	if err := db.Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).
		Order("from_path ASC, kind ASC, from_key ASC, to_path ASC, to_key ASC, id ASC").
		Limit(types.SourceWikiSkeletonMaxRelations + 1).Find(&relations).Error; err != nil {
		return result, nil, err
	}
	if len(relations) > types.SourceWikiSkeletonMaxRelations {
		return result, nil, fmt.Errorf("source Wiki impact relation inventory exceeds %d", types.SourceWikiSkeletonMaxRelations)
	}
	result.Relations = relations
	loaded.Relations = relations
	return result, loaded, nil
}
