package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceWikiStoredContribution struct {
	TopicKind           string                      `json:"topic_kind"`
	TopicKey            string                      `json:"topic_key"`
	Title               string                      `json:"title"`
	Summary             string                      `json:"summary"`
	Content             string                      `json:"content"`
	SourceRefs          types.StringArray           `json:"source_refs"`
	HasUnattributedBody *bool                       `json:"has_unattributed_body"`
	SourceProvenance    *types.SourceWikiProvenance `json:"source_provenance"`
}

type sourceWikiPageContributionProjection struct {
	rows           []types.SourceWikiPageContribution
	updated        types.SourceWikiContributionSet
	storedByKey    map[string]sourceWikiStoredContribution
	replacementKey string
	replacement    types.WikiPage
}

// projectSourceWikiContributionReplacementInTx replaces only the generated
// source/topic body, then renders the page from all validated stored bodies.
// Foreign source contributions are never reconstructed from page-level refs.
func (s *sourceWikiService) projectSourceWikiContributionReplacementInTx(tx *gorm.DB, existing, page *types.WikiPage,
	attempt *types.SourceWikiAttempt, inventoryComplete bool) (*sourceWikiPageContributionProjection, error) {
	if tx == nil || existing == nil || page == nil || attempt == nil || existing.ID == "" || existing.Version <= 0 {
		return nil, fmt.Errorf("source Wiki contribution replacement lacks page identity")
	}
	var current types.WikiPage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=?",
		existing.ID, attempt.TenantID, attempt.KnowledgeBaseID, existing.Version).Take(&current).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, repository.ErrWikiPageConflict
		}
		return nil, err
	}
	var rows []types.SourceWikiPageContribution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("page_id=? AND revision_id IS NULL", current.ID).
		Order("source_id ASC, topic_kind ASC, topic_key ASC").
		Limit(types.SourceWikiContributionMaxCount + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows) == 1 && rows[0].SourceID == attempt.SourceID &&
		rows[0].TopicKind == attempt.TopicKind && rows[0].TopicKey == attempt.TopicKey {
		return nil, nil
	}
	if len(rows) > types.SourceWikiContributionMaxCount || current.SourceProvenance == nil {
		return nil, fmt.Errorf("source Wiki mixed contribution page cannot be safely replaced")
	}
	set := types.SourceWikiContributionSet{TenantID: current.TenantID, KnowledgeBaseID: current.KnowledgeBaseID}
	storedByKey := make(map[string]sourceWikiStoredContribution, len(rows)+1)
	bodyParts := make([]string, 0, len(rows))
	allRefs := make([]string, 0)
	var primary *sourceWikiStoredContribution
	for i := range rows {
		row := rows[i]
		var stored sourceWikiStoredContribution
		if row.PageVersion != current.Version || (row.State != "ready" && row.State != "stale") || len(row.Contribution) == 0 ||
			json.Unmarshal(row.Contribution, &stored) != nil || stored.HasUnattributedBody == nil || *stored.HasUnattributedBody ||
			stored.SourceProvenance == nil || stored.TopicKind != row.TopicKind || stored.TopicKey != row.TopicKey ||
			stored.Title == "" || stored.Content == "" || stored.SourceRefs == nil {
			return nil, fmt.Errorf("source Wiki mixed contribution page has an invalid stored projection")
		}
		provenance := stored.SourceProvenance
		if provenance.SourceID != row.SourceID || provenance.TopicKind != row.TopicKind || provenance.TopicKey != row.TopicKey ||
			provenance.ApplicableSnapshotID != row.ApplicableSnapshotID || provenance.State != row.State ||
			(provenance.State != "ready" && provenance.State != "stale") || len(provenance.Evidence) == 0 {
			return nil, fmt.Errorf("source Wiki mixed contribution provenance does not match its stored row")
		}
		origin := provenance.Evidence[0].SnapshotID
		if origin == "" {
			return nil, fmt.Errorf("source Wiki mixed contribution has no evidence origin snapshot")
		}
		for _, evidence := range provenance.Evidence {
			if evidence.DataSourceID != row.SourceID || evidence.SnapshotID != origin || evidence.ID == "" ||
				evidence.KnowledgeID == "" || evidence.FileVersionID == "" || evidence.Path == "" {
				return nil, fmt.Errorf("source Wiki mixed contribution evidence crosses its source or origin snapshot")
			}
		}
		state := types.SourceWikiContributionCurrent
		if row.State == "stale" {
			state = types.SourceWikiContributionStale
		}
		set.Contributions = append(set.Contributions, types.SourceWikiContribution{
			SourceID: row.SourceID, TopicKind: row.TopicKind, TopicKey: row.TopicKey,
			OriginSnapshotID: origin, ApplicableSnapshotID: row.ApplicableSnapshotID,
			Body: stored.Content, Evidence: append([]types.SourceWikiEvidence(nil), provenance.Evidence...), State: state,
		})
		key := sourceWikiContributionStorageKey(row.SourceID, row.TopicKind, row.TopicKey)
		if _, exists := storedByKey[key]; exists {
			return nil, fmt.Errorf("source Wiki mixed contribution page has duplicate source/topic identity")
		}
		storedByKey[key] = stored
		bodyParts = append(bodyParts, stored.Content)
		allRefs = append(allRefs, stored.SourceRefs...)
		if current.SourceProvenance.SourceID == row.SourceID && current.SourceProvenance.TopicKind == row.TopicKind &&
			current.SourceProvenance.TopicKey == row.TopicKey {
			copy := stored
			primary = &copy
		}
	}
	if primary == nil || !equalSortedSourceWikiIDs(current.SourceRefs, allRefs) ||
		!sourceWikiBodyCompositionMatches(current.Content, bodyParts) || current.Title != primary.Title || current.Summary != primary.Summary {
		return nil, fmt.Errorf("source Wiki mixed contribution page content is not an exact contribution projection")
	}
	pageProvenance, err := json.Marshal(current.SourceProvenance)
	if err != nil {
		return nil, err
	}
	primaryProvenance, err := json.Marshal(primary.SourceProvenance)
	if err != nil {
		return nil, err
	}
	if !equalJSONBytes(pageProvenance, primaryProvenance) {
		return nil, fmt.Errorf("source Wiki mixed contribution page primary provenance is inconsistent")
	}

	replacementProvenance := cloneSourceWikiProvenance(page.SourceProvenance)
	if replacementProvenance == nil || replacementProvenance.SourceID != attempt.SourceID ||
		replacementProvenance.TopicKind != attempt.TopicKind || replacementProvenance.TopicKey != attempt.TopicKey ||
		replacementProvenance.ApplicableSnapshotID != attempt.SnapshotID || replacementProvenance.State != "ready" ||
		len(replacementProvenance.Evidence) == 0 {
		return nil, fmt.Errorf("source Wiki replacement provenance does not match its fenced attempt")
	}
	replacementOrigin := replacementProvenance.Evidence[0].SnapshotID
	replacement := types.SourceWikiContribution{
		SourceID: replacementProvenance.SourceID, TopicKind: replacementProvenance.TopicKind,
		TopicKey: replacementProvenance.TopicKey, OriginSnapshotID: replacementOrigin,
		ApplicableSnapshotID: replacementProvenance.ApplicableSnapshotID, Body: page.Content,
		Evidence: append([]types.SourceWikiEvidence(nil), replacementProvenance.Evidence...), State: types.SourceWikiContributionCurrent,
	}
	target := types.SourceWikiContributionTarget{
		TenantID: current.TenantID, KnowledgeBaseID: current.KnowledgeBaseID, SourceID: attempt.SourceID,
		TopicKind: attempt.TopicKind, TopicKey: attempt.TopicKey, SnapshotID: attempt.SnapshotID,
	}
	updated, err := source.ReplaceSourceWikiContribution(set, target, types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &replacement,
	})
	if err != nil {
		return nil, fmt.Errorf("replace source Wiki contribution: %w", err)
	}
	if !inventoryComplete {
		replacementProvenance.State = "stale"
		for i := range updated.Contributions {
			if sourceWikiContributionStorageKey(updated.Contributions[i].SourceID, updated.Contributions[i].TopicKind,
				updated.Contributions[i].TopicKey) == sourceWikiContributionStorageKey(attempt.SourceID, attempt.TopicKind, attempt.TopicKey) {
				updated.Contributions[i].State = types.SourceWikiContributionStale
				break
			}
		}
	}
	replacementRefsInput := make([]string, 0, len(replacementProvenance.Evidence))
	for _, evidence := range replacementProvenance.Evidence {
		replacementRefsInput = append(replacementRefsInput, evidence.KnowledgeID+"|"+evidence.Path)
	}
	replacementRefs := sortedSourceWikiIDs(replacementRefsInput)
	unattributedBody := false
	replacementKey := sourceWikiContributionStorageKey(attempt.SourceID, attempt.TopicKind, attempt.TopicKey)
	replacementStored := sourceWikiStoredContribution{
		TopicKind: attempt.TopicKind, TopicKey: attempt.TopicKey, Title: page.Title, Summary: page.Summary,
		Content: page.Content, SourceRefs: replacementRefs,
		HasUnattributedBody: &unattributedBody, SourceProvenance: replacementProvenance,
	}
	storedByKey[replacementKey] = replacementStored
	var projectedBodies, projectedRefs []string
	for _, contribution := range updated.Contributions {
		stored, ok := storedByKey[sourceWikiContributionStorageKey(contribution.SourceID, contribution.TopicKind, contribution.TopicKey)]
		if !ok {
			return nil, fmt.Errorf("source Wiki replacement projection lost a stored contribution")
		}
		projectedBodies = append(projectedBodies, contribution.Body)
		projectedRefs = append(projectedRefs, stored.SourceRefs...)
	}
	if len(projectedBodies) > 16 {
		sort.Strings(projectedBodies)
	}
	primaryContribution := updated.Contributions[0]
	projectedPrimary := storedByKey[sourceWikiContributionStorageKey(primaryContribution.SourceID, primaryContribution.TopicKind, primaryContribution.TopicKey)]
	page.Title, page.Summary = projectedPrimary.Title, projectedPrimary.Summary
	page.Content = strings.Join(projectedBodies, "\n")
	page.SourceRefs = sortedSourceWikiIDs(projectedRefs)
	page.SourceProvenance = cloneSourceWikiProvenance(projectedPrimary.SourceProvenance)
	page.LastEditSource = types.WikiEditSourcePipeline
	wiki, ok := s.wiki.(*wikiPageService)
	if !ok {
		return nil, fmt.Errorf("source Wiki mixed contribution page cannot recalculate links")
	}
	page.OutLinks = wiki.parseOutLinks(page.Content)
	replacementPage := *page
	replacementPage.Content = replacement.Body
	replacementPage.Title = replacementStored.Title
	replacementPage.Summary = replacementStored.Summary
	replacementPage.SourceRefs = append(types.StringArray(nil), replacementStored.SourceRefs...)
	replacementPage.SourceProvenance = cloneSourceWikiProvenance(replacementProvenance)
	return &sourceWikiPageContributionProjection{
		rows: rows, updated: updated, storedByKey: storedByKey,
		replacementKey: replacementKey,
		replacement:    replacementPage,
	}, nil
}

func (p *sourceWikiPageContributionProjection) persistInTx(tx *gorm.DB, page *types.WikiPage, attempt *types.SourceWikiAttempt,
	dependencyIDs, moduleMemberIDs []string, inventoryComplete bool, now time.Time) error {
	if p == nil || tx == nil || page == nil || attempt == nil || page.Version <= 0 {
		return fmt.Errorf("source Wiki contribution projection persistence is incomplete")
	}
	replacementPage := p.replacement
	replacementPage.ID, replacementPage.Version = page.ID, page.Version
	if err := repository.PersistSourceWikiPageContributionInTx(tx, &replacementPage, attempt.TopicKind, attempt.TopicKey,
		dependencyIDs, moduleMemberIDs, inventoryComplete, now); err != nil {
		return err
	}
	rowsByKey := make(map[string]types.SourceWikiPageContribution, len(p.rows))
	for _, row := range p.rows {
		rowsByKey[sourceWikiContributionStorageKey(row.SourceID, row.TopicKind, row.TopicKey)] = row
	}
	for _, contribution := range p.updated.Contributions {
		key := sourceWikiContributionStorageKey(contribution.SourceID, contribution.TopicKind, contribution.TopicKey)
		if key == p.replacementKey {
			continue
		}
		row, ok := rowsByKey[key]
		if !ok {
			return fmt.Errorf("source Wiki replacement projection lost a surviving row")
		}
		state := "ready"
		if contribution.State == types.SourceWikiContributionStale {
			state = "stale"
		}
		result := tx.Model(&types.SourceWikiPageContribution{}).
			Where("id=? AND page_id=? AND page_version=? AND revision_id IS NULL", row.ID, page.ID, page.Version).
			Updates(map[string]any{"page_version": page.Version, "state": state, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("surviving source Wiki contribution changed during replacement projection")
		}
	}
	if err := tx.Where("page_id=? AND revision_id IS NULL", page.ID).Delete(&types.SourceWikiEvidenceRef{}).Error; err != nil {
		return err
	}
	for _, contribution := range p.updated.Contributions {
		for _, evidence := range contribution.Evidence {
			ref := types.SourceWikiEvidenceRef{
				ID: uuid.NewString(), PageID: page.ID, Version: page.Version, EvidenceID: evidence.ID,
				SourceFileID: evidence.KnowledgeID, FileVersionID: evidence.FileVersionID,
				SnapshotID: evidence.SnapshotID, Path: evidence.Path, CommitSHA: evidence.CommitSHA,
			}
			if err := tx.Create(&ref).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// removeSourceWikiContributionInTx removes only an exact attributable body.
// Mixed pages are reprojected from the surviving contribution records; any
// manual text, legacy body, or inconsistent source-ref projection fails closed.
func (s *sourceWikiService) removeSourceWikiContributionInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload,
	row types.SourceWikiPageContribution, now time.Time) (bool, error) {
	var page types.WikiPage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=?",
		row.PageID, payload.TenantID, payload.KnowledgeBaseID, row.PageVersion).Take(&page).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	var rows []types.SourceWikiPageContribution
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("page_id=? AND revision_id IS NULL", row.PageID).
		Order("source_id ASC, topic_kind ASC, topic_key ASC").Limit(types.SourceWikiContributionMaxCount + 1).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 || len(rows) > types.SourceWikiContributionMaxCount || page.SourceProvenance == nil || row.PageVersion != page.Version {
		return false, nil
	}
	set := types.SourceWikiContributionSet{TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID}
	storedByIdentity := make(map[string]sourceWikiStoredContribution, len(rows))
	bodyParts := make([]string, 0, len(rows))
	allRefs := make([]string, 0)
	var primary *sourceWikiStoredContribution
	removedFound := false
	for i := range rows {
		candidate := rows[i]
		var stored sourceWikiStoredContribution
		if candidate.PageVersion != page.Version || candidate.TargetSnapshotID != candidate.ApplicableSnapshotID ||
			candidate.State != "ready" && candidate.State != "stale" || len(candidate.Contribution) == 0 ||
			json.Unmarshal(candidate.Contribution, &stored) != nil || stored.HasUnattributedBody == nil || *stored.HasUnattributedBody ||
			stored.SourceProvenance == nil || stored.TopicKind != candidate.TopicKind || stored.TopicKey != candidate.TopicKey ||
			stored.Title == "" || stored.Content == "" || stored.SourceRefs == nil {
			return false, nil
		}
		provenance := stored.SourceProvenance
		if provenance.SourceID != candidate.SourceID || provenance.TopicKind != candidate.TopicKind || provenance.TopicKey != candidate.TopicKey ||
			provenance.ApplicableSnapshotID != candidate.ApplicableSnapshotID || provenance.State != candidate.State ||
			(provenance.State != "ready" && provenance.State != "stale") || len(provenance.Evidence) == 0 {
			return false, nil
		}
		origin := provenance.Evidence[0].SnapshotID
		if origin == "" {
			return false, nil
		}
		for _, evidence := range provenance.Evidence {
			if evidence.DataSourceID != candidate.SourceID || evidence.SnapshotID != origin || evidence.ID == "" || evidence.KnowledgeID == "" ||
				evidence.FileVersionID == "" || evidence.Path == "" {
				return false, nil
			}
		}
		state := types.SourceWikiContributionCurrent
		if candidate.State == "stale" {
			state = types.SourceWikiContributionStale
		}
		set.Contributions = append(set.Contributions, types.SourceWikiContribution{
			SourceID: candidate.SourceID, TopicKind: candidate.TopicKind, TopicKey: candidate.TopicKey,
			OriginSnapshotID: origin, ApplicableSnapshotID: candidate.ApplicableSnapshotID,
			Body: stored.Content, Evidence: provenance.Evidence, State: state,
		})
		identity := sourceWikiContributionStorageKey(candidate.SourceID, candidate.TopicKind, candidate.TopicKey)
		if _, exists := storedByIdentity[identity]; exists {
			return false, nil
		}
		storedByIdentity[identity] = stored
		bodyParts = append(bodyParts, stored.Content)
		allRefs = append(allRefs, stored.SourceRefs...)
		if candidate.ID == row.ID && candidate.SourceID == payload.DataSourceID && candidate.TopicKind == row.TopicKind && candidate.TopicKey == row.TopicKey {
			removedFound = true
		}
		if page.SourceProvenance.SourceID == candidate.SourceID && page.SourceProvenance.TopicKind == candidate.TopicKind && page.SourceProvenance.TopicKey == candidate.TopicKey {
			copy := stored
			primary = &copy
		}
	}
	if !removedFound || primary == nil || !equalSortedSourceWikiIDs(page.SourceRefs, allRefs) || !sourceWikiBodyCompositionMatches(page.Content, bodyParts) ||
		page.Title != primary.Title || page.Summary != primary.Summary {
		return false, nil
	}
	oldPage := page
	oldPage.SourceProvenance = cloneSourceWikiProvenance(page.SourceProvenance)
	oldOutLinks := append(types.StringArray(nil), page.OutLinks...)
	primaryJSON, _ := json.Marshal(page.SourceProvenance)
	storedPrimaryJSON, _ := json.Marshal(primary.SourceProvenance)
	if !equalJSONBytes(primaryJSON, storedPrimaryJSON) {
		return false, nil
	}
	updated, err := source.ReplaceSourceWikiContribution(set, types.SourceWikiContributionTarget{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID, SourceID: payload.DataSourceID,
		TopicKind: row.TopicKind, TopicKey: row.TopicKey, SnapshotID: payload.SnapshotID,
	}, types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeConfirmedDeleted, CompleteSnapshotID: payload.SnapshotID})
	if err != nil {
		return false, nil
	}
	if len(updated.Contributions) == 0 {
		page.Status = types.WikiPageStatusArchived
		page.UpdatedAt = now
		page.SourceProvenance.State = "stale"
		writer := repository.NewWikiPageRepository(tx)
		revision := revisionFromPage(&oldPage)
		if err := writer.UpdateWithRevision(sourceWikiVerifiedWrite(types.WithExecutionTenant(context.Background(), payload.TenantID)), &page, revision); err != nil {
			return false, err
		}
		if err := deleteRemovedSourceWikiContribution(tx, row, page.Version); err != nil {
			return false, err
		}
		return true, nil
	}
	storedSurvivors := make(map[string]sourceWikiStoredContribution, len(updated.Contributions))
	var projectedRefs []string
	var projectedBodies []string
	for _, contribution := range updated.Contributions {
		key := sourceWikiContributionStorageKey(contribution.SourceID, contribution.TopicKind, contribution.TopicKey)
		stored, ok := storedByIdentity[key]
		if !ok {
			return false, nil
		}
		storedSurvivors[key] = stored
		projectedRefs = append(projectedRefs, stored.SourceRefs...)
		projectedBodies = append(projectedBodies, contribution.Body)
	}
	projectedPrimary := updated.Contributions[0]
	projectedStored := storedSurvivors[sourceWikiContributionStorageKey(projectedPrimary.SourceID, projectedPrimary.TopicKind, projectedPrimary.TopicKey)]
	page.Title, page.Summary, page.Content = projectedStored.Title, projectedStored.Summary, strings.Join(projectedBodies, "\n")
	page.SourceRefs = sortedSourceWikiIDs(projectedRefs)
	page.SourceProvenance = cloneSourceWikiProvenance(projectedStored.SourceProvenance)
	page.UpdatedAt = now
	page.LastEditSource = types.WikiEditSourcePipeline
	if wiki, ok := s.wiki.(*wikiPageService); ok {
		page.OutLinks = wiki.parseOutLinks(page.Content)
	} else {
		return false, nil
	}
	revision := revisionFromPage(&oldPage)
	writer := repository.NewWikiPageRepository(tx)
	if err := writer.UpdateWithRevision(sourceWikiVerifiedWrite(types.WithExecutionTenant(context.Background(), payload.TenantID)), &page, revision); err != nil {
		return false, err
	}
	if err := deleteRemovedSourceWikiContribution(tx, row, page.Version); err != nil {
		return false, err
	}
	for _, contribution := range updated.Contributions {
		key := sourceWikiContributionStorageKey(contribution.SourceID, contribution.TopicKind, contribution.TopicKey)
		var storedJSON []byte
		for _, candidate := range rows {
			if sourceWikiContributionStorageKey(candidate.SourceID, candidate.TopicKind, candidate.TopicKey) == key {
				storedJSON = candidate.Contribution
				break
			}
		}
		state := "ready"
		if contribution.State == types.SourceWikiContributionStale {
			state = "stale"
		}
		result := tx.Model(&types.SourceWikiPageContribution{}).
			Where("page_id=? AND source_id=? AND topic_kind=? AND topic_key=? AND revision_id IS NULL", page.ID, contribution.SourceID, contribution.TopicKind, contribution.TopicKey).
			Updates(map[string]any{"page_version": page.Version, "state": state, "contribution": types.JSON(storedJSON), "updated_at": now})
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, fmt.Errorf("surviving source Wiki contribution changed during page projection")
		}
	}
	if err := tx.Where("page_id=? AND revision_id IS NULL", page.ID).Delete(&types.SourceWikiEvidenceRef{}).Error; err != nil {
		return false, err
	}
	for _, contribution := range updated.Contributions {
		for _, evidence := range contribution.Evidence {
			ref := types.SourceWikiEvidenceRef{
				ID: uuid.NewString(), PageID: page.ID, Version: page.Version, EvidenceID: evidence.ID,
				SourceFileID: evidence.KnowledgeID, FileVersionID: evidence.FileVersionID,
				SnapshotID: evidence.SnapshotID, Path: evidence.Path, CommitSHA: evidence.CommitSHA,
			}
			if err := tx.Create(&ref).Error; err != nil {
				return false, err
			}
		}
	}
	if err := updateWikiBacklinksInTx(tx, page.KnowledgeBaseID, page.Slug, oldOutLinks, page.OutLinks); err != nil {
		return false, err
	}
	return true, nil
}

func sourceWikiContributionStorageKey(sourceID, topicKind, topicKey string) string {
	return sourceID + "\x00" + topicKind + "\x00" + topicKey
}

func sourceWikiBodyCompositionMatches(body string, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	if len(parts) > 16 {
		canonical := append([]string(nil), parts...)
		sort.Strings(canonical)
		return body == strings.Join(canonical, "\n")
	}
	seen := make(map[string]bool)
	var match func(int, uint32, int) bool
	match = func(offset int, used uint32, count int) bool {
		if count == len(parts) {
			return offset == len(body)
		}
		state := fmt.Sprintf("%d/%d", offset, used)
		if seen[state] {
			return false
		}
		seen[state] = true
		for i, part := range parts {
			bit := uint32(1) << i
			if used&bit != 0 || !strings.HasPrefix(body[offset:], part) {
				continue
			}
			next := offset + len(part)
			if count+1 == len(parts) {
				if match(next, used|bit, count+1) {
					return true
				}
			} else if next < len(body) && body[next] == '\n' && match(next+1, used|bit, count+1) {
				return true
			}
		}
		return false
	}
	return match(0, 0, 0)
}

func equalSortedSourceWikiIDs(a, b []string) bool {
	return strings.Join(sortedSourceWikiIDs(a), "\x00") == strings.Join(sortedSourceWikiIDs(b), "\x00")
}

func sortedSourceWikiIDs(ids []string) types.StringArray {
	result := append(types.StringArray(nil), ids...)
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

func cloneSourceWikiProvenance(value *types.SourceWikiProvenance) *types.SourceWikiProvenance {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Evidence = append([]types.SourceWikiEvidence(nil), value.Evidence...)
	return &copy
}

func equalJSONBytes(a, b []byte) bool {
	return string(a) == string(b)
}

func deleteRemovedSourceWikiContribution(tx *gorm.DB, row types.SourceWikiPageContribution, pageVersion int) error {
	result := tx.Where("id=? AND page_id=? AND source_id=? AND topic_kind=? AND topic_key=? AND page_version=? AND revision_id IS NULL",
		row.ID, row.PageID, row.SourceID, row.TopicKind, row.TopicKey, pageVersion).Delete(&types.SourceWikiPageContribution{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("removed source Wiki contribution changed while the page was being reprojected")
	}
	return nil
}

func updateWikiBacklinksInTx(tx *gorm.DB, knowledgeBaseID, pageSlug string, oldLinks, newLinks types.StringArray) error {
	oldSet, newSet := map[string]bool{}, map[string]bool{}
	for _, slug := range oldLinks {
		oldSet[slug] = true
	}
	for _, slug := range newLinks {
		newSet[slug] = true
	}
	for slug := range oldSet {
		if newSet[slug] {
			continue
		}
		var target types.WikiPage
		if err := tx.Where("knowledge_base_id=? AND slug=? AND status<>?", knowledgeBaseID, slug, types.WikiPageStatusArchived).Take(&target).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		target.InLinks = removeWikiString(target.InLinks, pageSlug)
		if err := tx.Model(&types.WikiPage{}).Where("id=?", target.ID).Update("in_links", target.InLinks).Error; err != nil {
			return err
		}
	}
	for slug := range newSet {
		if oldSet[slug] {
			continue
		}
		var target types.WikiPage
		if err := tx.Where("knowledge_base_id=? AND slug=? AND status<>?", knowledgeBaseID, slug, types.WikiPageStatusArchived).Take(&target).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}
		target.InLinks = appendWikiString(target.InLinks, pageSlug)
		if err := tx.Model(&types.WikiPage{}).Where("id=?", target.ID).Update("in_links", target.InLinks).Error; err != nil {
			return err
		}
	}
	return nil
}

func removeWikiString(values types.StringArray, target string) types.StringArray {
	result := make(types.StringArray, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func appendWikiString(values types.StringArray, target string) types.StringArray {
	for _, value := range values {
		if value == target {
			return values
		}
	}
	return append(values, target)
}
