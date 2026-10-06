package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const sourceWikiContributionInventoryLimit = types.SourceWikiSkeletonMaxFiles

// sourceWikiContributionInventory records the complete file membership a
// freshly generated source-owned topic may depend on. A bounded but incomplete
// manifest is represented as incomplete, never as an empty proof of safety.
func sourceWikiContributionInventory(tx *gorm.DB, attempt *types.SourceWikiAttempt) (dependencyIDs, moduleMemberIDs []string, complete bool, err error) {
	if tx == nil || attempt == nil || attempt.SourceID == "" || attempt.SnapshotID == "" {
		return nil, nil, false, fmt.Errorf("source Wiki contribution inventory requires a fixed topic snapshot")
	}
	var snapshot types.SourceSnapshot
	if err := tx.Select("id,manifest_complete,member_count,relations_staged,relation_count,wiki_derivation_state").
		Where("id=? AND data_source_id=? AND tenant_id=? AND knowledge_base_id=?", attempt.SnapshotID, attempt.SourceID, attempt.TenantID, attempt.KnowledgeBaseID).
		Take(&snapshot).Error; err != nil {
		return nil, nil, false, err
	}
	if snapshot.WikiDerivationState == "deferred_capacity" {
		return nil, nil, false, fmt.Errorf("%w: source Wiki contributions cannot be generated for this snapshot", repository.ErrSourceWikiDerivationDeferred)
	}
	if snapshot.WikiDerivationState != "complete" || !snapshot.RelationsStaged {
		return nil, nil, false, fmt.Errorf("%w: source Wiki contributions require complete derivation", repository.ErrSourceWikiDerivationUnavailable)
	}
	var memberCount int64
	if err := tx.Model(&types.SourceSnapshotMember{}).Where("snapshot_id=?", attempt.SnapshotID).Count(&memberCount).Error; err != nil {
		return nil, nil, false, err
	}
	manifestComplete := snapshot.ManifestComplete && memberCount == int64(snapshot.MemberCount)
	if attempt.TopicKind == "module" || attempt.TopicKind == "system" || attempt.TopicKind == "" {
		type memberIdentity struct {
			Path                  string
			SourceFileID          string
			FileVersionID         string
			JoinedFileID          string
			JoinedTenantID        uint64
			JoinedKnowledgeBaseID string
			JoinedDataSourceID    string
			JoinedVersionID       string
			JoinedVersionFileID   string
		}
		var rows []memberIdentity
		query := tx.Table("source_snapshot_members sm").
			Joins("LEFT JOIN source_files sf ON sf.id=sm.source_file_id").
			Joins("LEFT JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.snapshot_id=sm.snapshot_id").
			Select(`sm.path, sm.source_file_id, sm.file_version_id, sf.id AS joined_file_id,
				sf.tenant_id AS joined_tenant_id, sf.knowledge_base_id AS joined_knowledge_base_id,
				sf.data_source_id AS joined_data_source_id, sv.id AS joined_version_id,
				sv.source_file_id AS joined_version_file_id`).
			Where("sm.snapshot_id=? AND sm.status='parsed'", attempt.SnapshotID)
		if attempt.TopicKind == "module" || attempt.TopicKind == "" {
			if attempt.ModulePath == "" {
				return nil, nil, false, fmt.Errorf("module contribution has no canonical path")
			}
			prefix := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(attempt.ModulePath)
			query = query.Where("sm.path LIKE ? ESCAPE '!'", prefix+"/%")
		}
		if err := query.Order("sm.path ASC").Limit(sourceWikiContributionInventoryLimit + 1).Find(&rows).Error; err != nil {
			return nil, nil, false, err
		}
		if len(rows) > sourceWikiContributionInventoryLimit {
			return nil, nil, false, nil
		}
		seenFiles, seenVersions := map[string]bool{}, map[string]bool{}
		for _, row := range rows {
			if row.Path == "" || row.SourceFileID == "" || row.FileVersionID == "" || row.JoinedFileID != row.SourceFileID ||
				row.JoinedTenantID != attempt.TenantID || row.JoinedKnowledgeBaseID != attempt.KnowledgeBaseID ||
				row.JoinedDataSourceID != attempt.SourceID || row.JoinedVersionID != row.FileVersionID ||
				row.JoinedVersionFileID != row.SourceFileID || seenFiles[row.SourceFileID] || seenVersions[row.FileVersionID] {
				return nil, nil, false, nil
			}
			seenFiles[row.SourceFileID], seenVersions[row.FileVersionID] = true, true
			moduleMemberIDs = append(moduleMemberIDs, row.SourceFileID)
		}
		if attempt.TopicKind == "system" || attempt.TopicKind == "" {
			dependencyIDs = append([]string(nil), moduleMemberIDs...)
		}
		return dependencyIDs, moduleMemberIDs, manifestComplete, nil
	}
	if attempt.TopicKind == "flow" {
		if !manifestComplete || !snapshot.RelationsStaged {
			return nil, nil, false, nil
		}
		var relationCount int64
		if err := tx.Model(&types.SourceCodeRelation{}).Where("tenant_id=? AND data_source_id=? AND snapshot_id=?",
			attempt.TenantID, attempt.SourceID, attempt.SnapshotID).Count(&relationCount).Error; err != nil {
			return nil, nil, false, err
		}
		if relationCount != int64(snapshot.RelationCount) || relationCount > types.SourceWikiSkeletonMaxRelations {
			return nil, nil, false, nil
		}
		var checkpoint sourceWikiAttemptCheckpoint
		if len(attempt.Checkpoint) == 0 || json.Unmarshal(attempt.Checkpoint, &checkpoint) != nil ||
			len(checkpoint.Relations) > sourceWikiMaxFlowRelations {
			return nil, nil, false, nil
		}
		seen := map[string]struct{}{}
		for _, relation := range checkpoint.Relations {
			if relation.TenantID != attempt.TenantID || relation.DataSourceID != attempt.SourceID || relation.SnapshotID != attempt.SnapshotID {
				return nil, nil, false, fmt.Errorf("flow contribution relation is bound to another source snapshot")
			}
			for _, id := range []string{relation.FromFileID, relation.ToFileID} {
				if id != "" {
					seen[id] = struct{}{}
				}
			}
			var refs []types.SourceRelationFactRef
			if len(relation.Context) > 0 && string(relation.Context) != "null" {
				if err := json.Unmarshal(relation.Context, &refs); err != nil {
					return nil, nil, false, fmt.Errorf("flow contribution causal fact inventory is invalid")
				}
			}
			for _, ref := range refs {
				if ref.DataSourceID != attempt.SourceID || ref.SnapshotID != attempt.SnapshotID || ref.FileID == "" || ref.FileVersionID == "" || ref.Path == "" {
					return nil, nil, false, fmt.Errorf("flow contribution causal fact is bound to another source snapshot")
				}
				seen[ref.FileID] = struct{}{}
			}
		}
		for id := range seen {
			dependencyIDs = append(dependencyIDs, id)
		}
		dependencyIDs = sourceWikiSortedUniqueIDs(dependencyIDs)
		return dependencyIDs, nil, true, nil
	}
	return nil, nil, false, fmt.Errorf("source Wiki contribution has an unsupported topic kind")
}

func sourceWikiSortedUniqueIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	result := append([]string(nil), ids...)
	sort.Strings(result)
	write := 0
	for _, id := range result {
		if id == "" || write > 0 && result[write-1] == id {
			continue
		}
		result[write] = id
		write++
	}
	return result[:write]
}
