package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sourceWikiMaxEvidenceBytes             = 32768
	sourceWikiMaxFiles                     = 16
	sourceWikiFlowEvidenceWindowBytes      = 4096
	sourceWikiFlowEvidenceContextBytes     = 512
	sourceWikiMaxFlowEvidenceWindowRecords = sourceWikiFlowDiagramMaxEdges * 2
)

type sourceWikiFlowEvidenceTarget struct {
	FileID    string
	VersionID string
	Path      string
}

func sourceWikiFlowEvidenceRanges(relations []types.SourceCodeRelation, sourceID, snapshotID string) (map[sourceWikiFlowEvidenceTarget][]types.SourceRange, error) {
	targets := make(map[sourceWikiFlowEvidenceTarget][]types.SourceRange)
	addEndpoint := func(fileID, versionID, filePath string, rawRange types.JSON) error {
		if fileID == "" || versionID == "" || filePath == "" {
			return fmt.Errorf("flow relation has an incomplete exact source endpoint")
		}
		parsed, err := parseSourceWikiFlowRange(rawRange)
		if err != nil {
			return fmt.Errorf("flow relation lacks a valid exact source range: %w", err)
		}
		key := sourceWikiFlowEvidenceTarget{FileID: fileID, VersionID: versionID, Path: filePath}
		for _, existing := range targets[key] {
			if existing == parsed {
				return nil
			}
		}
		targets[key] = append(targets[key], parsed)
		return nil
	}
	for _, relation := range relations {
		if relation.DataSourceID != sourceID || relation.SnapshotID != snapshotID {
			return nil, fmt.Errorf("flow relation belongs to a different published source snapshot")
		}
		if err := addEndpoint(relation.FromFileID, relation.FromVersionID, relation.FromPath, relation.FromRange); err != nil {
			return nil, err
		}
		toHasFile := relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != ""
		if toHasFile {
			if err := addEndpoint(relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToRange); err != nil {
				return nil, err
			}
		} else if !sourceWikiFlowHasNoTargetRange(relation.ToRange) {
			return nil, fmt.Errorf("flow relation has a target range without an exact source endpoint")
		}
		if relation.Kind == "http_route" && len(relation.Context) > 0 {
			var refs []types.SourceRelationFactRef
			if err := json.Unmarshal(relation.Context, &refs); err != nil || refs == nil {
				return nil, fmt.Errorf("HTTP route relation fact references are invalid")
			}
			for _, ref := range refs {
				if ref.DataSourceID != sourceID || ref.SnapshotID != snapshotID {
					return nil, fmt.Errorf("HTTP route relation fact reference crosses the fixed source snapshot")
				}
				rawRange, err := json.Marshal(ref.Range)
				if err != nil {
					return nil, fmt.Errorf("HTTP route relation fact range is invalid")
				}
				if err := addEndpoint(ref.FileID, ref.FileVersionID, ref.Path, types.JSON(rawRange)); err != nil {
					return nil, fmt.Errorf("HTTP route relation fact reference is incomplete: %w", err)
				}
			}
		}
	}
	return targets, nil
}

func sourceWikiFlowEvidenceWindows(raw []byte, required []types.SourceRange) ([]types.SourceRange, error) {
	if len(required) == 0 {
		return nil, nil
	}
	ranges := append([]types.SourceRange(nil), required...)
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].StartByte != ranges[j].StartByte {
			return ranges[i].StartByte < ranges[j].StartByte
		}
		return ranges[i].EndByte < ranges[j].EndByte
	})
	windows := make([]types.SourceRange, 0, len(ranges))
	for _, requiredRange := range ranges {
		if requiredRange.StartByte < 0 || requiredRange.EndByte <= requiredRange.StartByte || requiredRange.EndByte > len(raw) ||
			requiredRange.StartLine < 1 || requiredRange.EndLine < requiredRange.StartLine ||
			requiredRange.StartLine != 1+bytes.Count(raw[:requiredRange.StartByte], []byte("\n")) ||
			requiredRange.EndLine != 1+bytes.Count(raw[:requiredRange.EndByte-1], []byte("\n")) ||
			!utf8.Valid(raw[requiredRange.StartByte:requiredRange.EndByte]) {
			return nil, fmt.Errorf("flow relation range does not match exact UTF-8 source coordinates")
		}
		targetBytes := requiredRange.EndByte - requiredRange.StartByte
		if targetBytes > sourceWikiFlowEvidenceWindowBytes {
			return nil, fmt.Errorf("flow relation range exceeds the bounded %d-byte evidence window", sourceWikiFlowEvidenceWindowBytes)
		}
		remaining := sourceWikiFlowEvidenceWindowBytes - targetBytes
		leftBudget := min(sourceWikiFlowEvidenceContextBytes, remaining/2)
		rightBudget := min(sourceWikiFlowEvidenceContextBytes, remaining-leftBudget)
		start := sourceWikiPreviousUTF8Boundary(raw, requiredRange.StartByte, leftBudget)
		end := sourceWikiNextUTF8Boundary(raw, requiredRange.EndByte, rightBudget)
		if !utf8.Valid(raw[start:end]) {
			start, end = requiredRange.StartByte, requiredRange.EndByte
		}
		if len(windows) > 0 && start <= windows[len(windows)-1].EndByte && end-windows[len(windows)-1].StartByte <= sourceWikiFlowEvidenceWindowBytes {
			previous := windows[len(windows)-1]
			windows[len(windows)-1] = sourceWikiRangeForBytes(raw, min(previous.StartByte, start), max(previous.EndByte, end))
			continue
		}
		windows = append(windows, sourceWikiRangeForBytes(raw, start, end))
	}
	return windows, nil
}

func sourceWikiPreviousUTF8Boundary(raw []byte, end, maxBytes int) int {
	start := end
	used := 0
	for start > 0 {
		_, size := utf8.DecodeLastRune(raw[:start])
		if size == 1 && raw[start-1] >= utf8.RuneSelf || used+size > maxBytes {
			break
		}
		start -= size
		used += size
	}
	return start
}

func sourceWikiNextUTF8Boundary(raw []byte, start, maxBytes int) int {
	end := start
	used := 0
	for end < len(raw) {
		_, size := utf8.DecodeRune(raw[end:])
		if size == 1 && raw[end] >= utf8.RuneSelf || used+size > maxBytes {
			break
		}
		end += size
		used += size
	}
	return end
}

func sourceWikiRangeForBytes(raw []byte, start, end int) types.SourceRange {
	return types.SourceRange{
		StartByte: start, EndByte: end,
		StartLine: 1 + bytes.Count(raw[:start], []byte("\n")),
		EndLine:   1 + bytes.Count(raw[:end-1], []byte("\n")),
	}
}

type sourceWikiService struct {
	wiki          interfaces.WikiPageService
	kb            interfaces.KnowledgeBaseService
	knowledge     interfaces.KnowledgeService
	models        interfaces.ModelService
	db            *gorm.DB
	batchMu       sync.Mutex
	batchWork     map[string]sourceWikiBatchWorker
	relationFacts sourceWikiRelationFactResolverCache
}

func NewSourceWikiService(wiki interfaces.WikiPageService, kb interfaces.KnowledgeBaseService, knowledge interfaces.KnowledgeService, models interfaces.ModelService, db *gorm.DB) interfaces.SourceWikiService {
	return &sourceWikiService{wiki: wiki, kb: kb, knowledge: knowledge, models: models, db: db, batchWork: make(map[string]sourceWikiBatchWorker)}
}

type sourceWikiVerifiedWriteKey struct{}

func sourceWikiVerifiedWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, sourceWikiVerifiedWriteKey{}, true)
}
func isSourceWikiVerifiedWrite(ctx context.Context) bool {
	verified, _ := ctx.Value(sourceWikiVerifiedWriteKey{}).(bool)
	return verified
}

type sourceWikiSection struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidence_ids"`
	Uncertain   bool     `json:"uncertain"`
}
type sourceWikiDraft struct {
	Title    string              `json:"title"`
	Summary  string              `json:"summary"`
	Sections []sourceWikiSection `json:"sections"`
}
type sourceWikiQA struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
	Sections  []int  `json:"sections"`
	Uncertain bool   `json:"uncertain"`
}
type sourceWikiPromptEvidence struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Quality string `json:"quality"`
}
type collectedWikiEvidence struct {
	Evidence types.SourceWikiEvidence
	Text     string
}

func sourceWikiJSON(text string, out any) error {
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("response must contain one JSON object")
	}
	return nil
}

func (s *sourceWikiService) collectEvidence(ctx context.Context, kbID, sourceID, module string) ([]collectedWikiEvidence, error) {
	attempt := &types.SourceWikiAttempt{SourceID: sourceID, ModulePath: module, TopicKind: "module", TopicKey: "module/" + module, ModelContextWindow: 65536, MaxCompletionTokens: types.SourceWikiAttemptMaxCompletionTokens}
	var publication types.SourcePublication
	if err := s.db.WithContext(ctx).Where("data_source_id = ? AND knowledge_base_id = ?", sourceID, kbID).Take(&publication).Error; err != nil {
		return nil, err
	}
	attempt.SnapshotID = publication.SnapshotID
	return s.collectTopicEvidence(ctx, kbID, attempt)
}

func (s *sourceWikiService) collectTopicEvidence(ctx context.Context, kbID string, attempt *types.SourceWikiAttempt) ([]collectedWikiEvidence, error) {
	if attempt == nil || attempt.SourceID == "" || attempt.SnapshotID == "" {
		return nil, fmt.Errorf("topic evidence requires its fixed source snapshot")
	}
	var members []types.SourceSnapshotMember
	var flowTargets map[sourceWikiFlowEvidenceTarget][]types.SourceRange
	q := s.db.WithContext(ctx).Table("source_snapshot_members sm").Select("sm.*").
		Joins("JOIN source_snapshots ss ON ss.id=sm.snapshot_id AND ss.data_source_id=? AND ss.knowledge_base_id=? AND ss.state='published'", attempt.SourceID, kbID).
		Where("sm.status='parsed'").
		Where("sm.snapshot_id = ?", attempt.SnapshotID).
		Where(source.SnapshotSQL(ctx, "sm.snapshot_id", "ss.data_source_id", "sm.source_file_id"))
	if attempt.TopicKind != "flow" {
		q = q.Where("NOT sm.generated")
	}
	if attempt.TopicKind == "module" || attempt.TopicKind == "" {
		q = q.Where("sm.path LIKE ? ESCAPE '!'", strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(attempt.ModulePath)+"/%")
	} else if attempt.TopicKind == "flow" {
		var coverage types.SourceWikiCoverageTopic
		if err := s.db.WithContext(ctx).Where(
			"knowledge_base_id = ? AND source_id = ? AND topic_key = ? AND batch_id = ? AND snapshot_id = ? AND kind = 'flow'",
			kbID, attempt.SourceID, attempt.TopicKey, attempt.BatchID, attempt.SnapshotID,
		).Take(&coverage).Error; err != nil {
			return nil, err
		}
		var relations []types.SourceCodeRelation
		if err := json.Unmarshal(coverage.Relations, &relations); err != nil {
			return nil, fmt.Errorf("planned flow relations are invalid")
		}
		tenantID := attempt.TenantID
		if tenantID == 0 {
			var snapshot types.SourceSnapshot
			if err := s.db.WithContext(ctx).Where("id = ? AND data_source_id = ? AND knowledge_base_id = ? AND state = 'published'",
				attempt.SnapshotID, attempt.SourceID, kbID).Take(&snapshot).Error; err != nil {
				return nil, fmt.Errorf("flow evidence snapshot identity is unavailable")
			}
			tenantID = snapshot.TenantID
		}
		relations, err := s.resolveSourceWikiRelations(ctx, tenantID, kbID, attempt.SourceID, attempt.SnapshotID, nil, relations)
		if err != nil {
			return nil, fmt.Errorf("planned flow relation facts cannot be verified")
		}
		flowTargets, err = sourceWikiFlowEvidenceRanges(relations, attempt.SourceID, attempt.SnapshotID)
		if err != nil {
			return nil, err
		}
		if len(flowTargets) == 0 {
			return nil, nil
		}
		paths := make(map[string]struct{}, len(flowTargets))
		for target := range flowTargets {
			paths[target.Path] = struct{}{}
		}
		if len(paths) > sourceWikiMaxFiles {
			return nil, fmt.Errorf("flow evidence requires more than %d exact source files", sourceWikiMaxFiles)
		}
		pathList := make([]string, 0, len(paths))
		for filePath := range paths {
			pathList = append(pathList, filePath)
		}
		sort.Strings(pathList)
		q = q.Where("sm.path IN ?", pathList)
	} else if attempt.TopicKind != "system" {
		return nil, fmt.Errorf("topic evidence requires a supported stable topic kind")
	}
	priority := `CASE WHEN lower(sm.path) LIKE '%readme%' THEN 0
		WHEN lower(sm.path) LIKE '%/pom.xml' OR lower(sm.path) LIKE '%/go.mod' OR lower(sm.path) LIKE '%/package.json' OR lower(sm.path) LIKE '%/build.gradle%' THEN 1
		WHEN lower(sm.path) LIKE '%/main.%' OR lower(sm.path) LIKE '%/application.%' THEN 2
		WHEN lower(sm.path) LIKE '%/route%' OR lower(sm.path) LIKE '%/controller%' OR lower(sm.path) LIKE '%/service%' THEN 3
		ELSE 10 END, sm.path`
	memberLimit := sourceWikiMaxFiles
	if attempt.TopicKind == "flow" {
		memberLimit++
	}
	if err := q.Order(priority).Limit(memberLimit).Find(&members).Error; err != nil {
		return nil, err
	}
	if attempt.TopicKind == "flow" {
		if len(members) > sourceWikiMaxFiles {
			return nil, fmt.Errorf("flow evidence exceeds the %d-file collection bound", sourceWikiMaxFiles)
		}
		found := make(map[sourceWikiFlowEvidenceTarget]bool, len(members))
		for _, member := range members {
			found[sourceWikiFlowEvidenceTarget{FileID: member.SourceFileID, VersionID: member.FileVersionID, Path: member.Path}] = true
		}
		for target := range flowTargets {
			if !found[target] {
				return nil, fmt.Errorf("flow relation endpoint is not a readable member of the fixed published snapshot")
			}
		}
	}
	result := make([]collectedWikiEvidence, 0, len(members))
	inputTokenBudget := attempt.ModelContextWindow - attempt.MaxCompletionTokens - 512
	if inputTokenBudget <= 0 {
		return nil, errSourceWikiAttemptCallContext
	}
	maxBytes := inputTokenBudget * 3
	if maxBytes > sourceWikiMaxEvidenceBytes {
		maxBytes = sourceWikiMaxEvidenceBytes
	}
	total := 0
	for _, member := range members {
		file, err := s.knowledge.GetSourceFile(ctx, member.SourceFileID, member.FileVersionID)
		if err != nil {
			return nil, fmt.Errorf("fixed topic evidence cannot be read")
		}
		if file.KnowledgeID != member.SourceFileID || file.FileVersionID != member.FileVersionID ||
			file.SnapshotID != member.SnapshotID || file.DataSourceID != attempt.SourceID || file.Path != member.Path {
			return nil, fmt.Errorf("fixed topic evidence changed")
		}
		if len(file.RawContent) == 0 {
			if attempt.TopicKind == "flow" {
				return nil, fmt.Errorf("flow relation endpoint has no readable source text")
			}
			continue
		}
		appendEvidence := func(evidenceRange types.SourceRange) error {
			if evidenceRange.StartByte < 0 || evidenceRange.EndByte <= evidenceRange.StartByte || evidenceRange.EndByte > len(file.RawContent) {
				return fmt.Errorf("source evidence range is outside the fixed file version")
			}
			text := file.RawContent[evidenceRange.StartByte:evidenceRange.EndByte]
			if !utf8.Valid(text) {
				return fmt.Errorf("source evidence window is not valid UTF-8")
			}
			if total+len(text) > maxBytes {
				return fmt.Errorf("complete source evidence exceeds the bounded model context")
			}
			hash := sha256.Sum256(text)
			evidence := types.SourceWikiEvidence{
				ID: fmt.Sprintf("e%03d", len(result)+1), KnowledgeID: file.KnowledgeID, SHA256: file.SHA256,
				TextSHA256: hex.EncodeToString(hash[:]),
				SourceEvidence: types.SourceEvidence{
					DataSourceID: file.DataSourceID, SnapshotID: file.SnapshotID, FileVersionID: file.FileVersionID,
					ProjectID: file.ProjectID, CommitSHA: file.CommitSHA, Path: file.Path, Quality: file.Quality,
					Range: evidenceRange,
				},
			}
			evidence.GitLabURL = source.GitLabBlobURL(file.RepositoryURL, evidence.CommitSHA, evidence.Path, evidence.Range)
			result = append(result, collectedWikiEvidence{Evidence: evidence, Text: string(text)})
			total += len(text)
			return nil
		}
		if attempt.TopicKind == "flow" {
			rawHash := sha256.Sum256(file.RawContent)
			if hex.EncodeToString(rawHash[:]) != file.SHA256 {
				return nil, fmt.Errorf("flow relation endpoint raw source hash changed")
			}
			target := sourceWikiFlowEvidenceTarget{FileID: member.SourceFileID, VersionID: member.FileVersionID, Path: member.Path}
			windows, err := sourceWikiFlowEvidenceWindows(file.RawContent, flowTargets[target])
			if err != nil {
				return nil, fmt.Errorf("cannot include complete flow relation evidence: %w", err)
			}
			if len(result)+len(windows) > sourceWikiMaxFlowEvidenceWindowRecords {
				return nil, fmt.Errorf("flow evidence exceeds the %d-window collection bound", sourceWikiMaxFlowEvidenceWindowRecords)
			}
			for _, evidenceRange := range windows {
				if err := appendEvidence(evidenceRange); err != nil {
					return nil, fmt.Errorf("cannot include complete flow relation evidence: %w", err)
				}
			}
			continue
		}
		remaining := maxBytes - total
		if remaining <= 0 {
			break
		}
		fileBudget := min(2048, remaining)
		end := min(len(file.RawContent), fileBudget)
		for end > 0 && !utf8.Valid(file.RawContent[:end]) {
			end--
		}
		if end <= 0 {
			continue
		}
		if err := appendEvidence(sourceWikiRangeForBytes(file.RawContent, 0, end)); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *sourceWikiService) validateEvidence(ctx context.Context, draft sourceWikiDraft, registry map[string]collectedWikiEvidence) ([]string, error) {
	if strings.TrimSpace(draft.Title) == "" || strings.TrimSpace(draft.Summary) == "" || len(draft.Sections) == 0 || len(draft.Sections) > 12 {
		return nil, fmt.Errorf("card needs supported title, summary and bounded sections")
	}
	used := map[string]bool{}
	for _, section := range draft.Sections {
		if strings.TrimSpace(section.Text) == "" || len(section.EvidenceIDs) == 0 {
			return nil, fmt.Errorf("every section requires evidence")
		}
		for _, id := range section.EvidenceIDs {
			if _, ok := registry[id]; !ok {
				return nil, fmt.Errorf("fabricated evidence ID %q", id)
			}
			used[id] = true
		}
	}
	// Title, summary and QA consume every file; validate that complete set again.
	for _, e := range registry {
		file, err := s.knowledge.GetSourceFile(ctx, e.Evidence.KnowledgeID, e.Evidence.FileVersionID)
		if err != nil {
			return nil, fmt.Errorf("evidence lost permission or fixed version")
		}
		raw := file.RawContent
		hash := sha256.Sum256(raw)
		r := e.Evidence.Range
		if file.CommitSHA != e.Evidence.CommitSHA || file.SnapshotID != e.Evidence.SnapshotID || file.FileVersionID != e.Evidence.FileVersionID || file.Path != e.Evidence.Path || file.SHA256 != e.Evidence.SHA256 || hex.EncodeToString(hash[:]) != e.Evidence.SHA256 || r.StartByte < 0 || r.EndByte > len(raw) || r.EndByte <= r.StartByte {
			return nil, fmt.Errorf("evidence SHA/hash/raw range mismatch")
		}
		hash = sha256.Sum256(raw[r.StartByte:r.EndByte])
		if hex.EncodeToString(hash[:]) != e.Evidence.TextSHA256 || r.StartLine != 1+bytes.Count(raw[:r.StartByte], []byte("\n")) || r.EndLine != 1+bytes.Count(raw[:r.EndByte-1], []byte("\n")) {
			return nil, fmt.Errorf("evidence original coordinates mismatch")
		}
	}
	ids := make([]string, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func sourceWikiCoverageSchemaAvailable(tx *gorm.DB) (bool, error) {
	var exists bool
	err := tx.Raw("SELECT to_regclass(?) IS NOT NULL", "source_wiki_topics").Scan(&exists).Error
	return exists, err
}

func sourceWikiLockTerminalFailedInitialCoverage(tx *gorm.DB, attempt *types.SourceWikiAttempt) (*types.SourceWikiCoverageTopic, error) {
	if tx == nil || attempt == nil || attempt.BatchID != "" || attempt.TopicKind != "module" ||
		attempt.TopicKey != "module/"+attempt.ModulePath || attempt.ModulePath == "" {
		return nil, nil
	}
	type coverageRef struct {
		ID      string
		BatchID *string
	}
	var ref coverageRef
	err := tx.Model(&types.SourceWikiCoverageTopic{}).Select("id", "batch_id").Where(
		"tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND topic_key = ? AND kind = 'module' AND module_path = ? AND title = ? AND snapshot_id = ? AND wiki_slug = ? AND initial = TRUE AND status IN ('failed', 'insufficient_evidence')",
		attempt.TenantID, attempt.KnowledgeBaseID, attempt.SourceID, attempt.TopicKey, attempt.ModulePath, attempt.Title, attempt.SnapshotID, attempt.Slug,
	).Take(&ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ref.BatchID == nil || *ref.BatchID == "" {
		return nil, nil
	}
	var batch types.SourceWikiBatch
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *ref.BatchID).Take(&batch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if batch.TenantID != attempt.TenantID || batch.KnowledgeBaseID != attempt.KnowledgeBaseID ||
		batch.SourceID != attempt.SourceID || batch.SnapshotID != attempt.SnapshotID ||
		(batch.Status != "completed" && batch.Status != "failed" && batch.Status != "expired") {
		return nil, nil
	}
	var topic types.SourceWikiCoverageTopic
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"id = ? AND tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND topic_key = ? AND kind = 'module' AND module_path = ? AND title = ? AND snapshot_id = ? AND wiki_slug = ? AND batch_id = ? AND initial = TRUE AND status IN ('failed', 'insufficient_evidence')",
		ref.ID, attempt.TenantID, attempt.KnowledgeBaseID, attempt.SourceID, attempt.TopicKey, attempt.ModulePath, attempt.Title, attempt.SnapshotID, attempt.Slug, batch.ID,
	).Take(&topic).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &topic, nil
}

func (s *sourceWikiService) publishCard(ctx context.Context, kb *types.KnowledgeBase, sourceID, snapshotID string, baseVersion int, existing, page *types.WikiPage, expectedSource *types.DataSource, expectedModel *types.Model, attempt *types.SourceWikiAttempt, lease types.SourceWikiAttemptLease) error {
	return s.publishCardWithBatchState(ctx, kb, sourceID, snapshotID, baseVersion, existing, page, expectedSource, expectedModel, attempt, lease, false, "")
}

func (s *sourceWikiService) publishStagedCard(ctx context.Context, kb *types.KnowledgeBase, sourceID, snapshotID string, baseVersion int, existing, page *types.WikiPage, expectedSource *types.DataSource, expectedModel *types.Model, attempt *types.SourceWikiAttempt, approvalDigest string) error {
	return s.publishCardWithBatchState(ctx, kb, sourceID, snapshotID, baseVersion, existing, page, expectedSource, expectedModel, attempt, types.SourceWikiAttemptLease{}, true, approvalDigest)
}

func (s *sourceWikiService) publishCardWithBatchState(ctx context.Context, kb *types.KnowledgeBase, sourceID, snapshotID string, baseVersion int, existing, page *types.WikiPage, expectedSource *types.DataSource, expectedModel *types.Model, attempt *types.SourceWikiAttempt, lease types.SourceWikiAttemptLease, stagedCandidate bool, approvalDigest string) error {
	if err := source.ValidateReadScope(ctx); err != nil {
		return err
	}
	// The publication lock serializes the final card write with source publish;
	// an old model result cannot become a current ready page after a new SHA.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var manualRepairTopic *types.SourceWikiCoverageTopic
		coverageSchemaAvailable := false
		if attempt.BatchID == "" && attempt.TopicKind == "module" && attempt.TopicKey == "module/"+attempt.ModulePath {
			var err error
			coverageSchemaAvailable, err = sourceWikiCoverageSchemaAvailable(tx)
			if err != nil {
				return err
			}
			if coverageSchemaAvailable {
				manualRepairTopic, err = sourceWikiLockTerminalFailedInitialCoverage(tx, attempt)
				if err != nil {
					return err
				}
			}
		}
		if attempt.BatchID != "" {
			if !stagedCandidate {
				return repository.ErrSourceWikiBatchInvalidState
			}
			var batch types.SourceWikiBatch
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.BatchID).Take(&batch).Error; err != nil {
				return repository.ErrSourceWikiBatchInvalidState
			}
			now := time.Now()
			if batch.Status != "running" || !now.Before(batch.DeadlineAt) ||
				batch.TenantID != attempt.TenantID || batch.KnowledgeBaseID != attempt.KnowledgeBaseID ||
				batch.SourceID != attempt.SourceID || batch.SnapshotID != attempt.SnapshotID {
				return repository.ErrSourceWikiBatchInvalidState
			}
			if batch.Phase != "publishing" || batch.QAApprovedAt == nil || batch.QAApprovalDigest == "" || batch.QAApprovalDigest != approvalDigest || attempt.StagedPageVersion != baseVersion {
				return repository.ErrSourceWikiBatchInvalidState
			}
			digest, err := repository.SourceWikiBatchCandidateDigestInTx(tx, &batch)
			if err != nil || digest != batch.QAApprovalDigest {
				return repository.ErrSourceWikiBatchInvalidState
			}
			var staged types.SourceWikiAttempt
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
				"id = ? AND batch_id = ? AND status = 'staged' AND staged_page_version = ?",
				attempt.ID, batch.ID, baseVersion,
			).Take(&staged).Error; err != nil {
				return repository.ErrSourceWikiAttemptFenced
			}
		} else if stagedCandidate {
			return repository.ErrSourceWikiBatchInvalidState
		}
		var currentSource types.DataSource
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=? AND knowledge_base_id=?", sourceID, kb.TenantID, kb.ID).First(&currentSource).Error; err != nil {
			return err
		}
		if string(currentSource.Config) != string(expectedSource.Config) || currentSource.Status != expectedSource.Status || !currentSource.UpdatedAt.Equal(expectedSource.UpdatedAt) || !currentSource.DeletedAt.Time.IsZero() {
			return fmt.Errorf("source configuration or lifecycle changed during module generation")
		}
		var currentKB types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=?", kb.ID, kb.TenantID).First(&currentKB).Error; err != nil {
			return err
		}
		oldWikiConfig, _ := json.Marshal(kb.WikiConfig)
		newWikiConfig, _ := json.Marshal(currentKB.WikiConfig)
		if !currentKB.IsWikiEnabled() || currentKB.SummaryModelID != kb.SummaryModelID || !bytes.Equal(oldWikiConfig, newWikiConfig) || !currentKB.UpdatedAt.Equal(kb.UpdatedAt) {
			return fmt.Errorf("Wiki configuration changed during module generation")
		}
		var currentModel types.Model
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=?", expectedModel.ID).First(&currentModel).Error; err != nil {
			return fmt.Errorf("Wiki model changed during module generation")
		}
		if !sourceWikiSameModel(expectedModel, &currentModel) {
			return fmt.Errorf("Wiki model changed during module generation")
		}
		var publication types.SourcePublication
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("data_source_id=? AND tenant_id=? AND knowledge_base_id=?", sourceID, kb.TenantID, kb.ID).Take(&publication).Error; err != nil {
			return err
		}
		if publication.SnapshotID != snapshotID {
			return fmt.Errorf("source changed during module generation; retry against the published version")
		}
		// Source/wiki writes are a single transaction, including first raw owners.
		transactionalWiki := NewWikiPageService(repository.NewWikiPageRepository(tx), nil, s.kb, nil, nil)
		writeCtx := sourceWikiVerifiedWrite(ctx)
		dependencyFileIDs, moduleMemberFileIDs, inventoryComplete, err := sourceWikiContributionInventory(tx, attempt)
		if err != nil {
			return err
		}
		var contributionProjection *sourceWikiPageContributionProjection
		var writeErr error
		if existing == nil {
			var written *types.WikiPage
			written, writeErr = transactionalWiki.CreatePage(writeCtx, page)
			if writeErr == nil {
				if written == nil {
					return fmt.Errorf("source Wiki page creation returned no persisted page")
				}
				*page = *written
			}
		} else {
			page.ID = existing.ID
			page.Version = baseVersion
			page.FolderID = existing.FolderID
			contributionProjection, err = s.projectSourceWikiContributionReplacementInTx(tx, existing, page, attempt, inventoryComplete)
			if err != nil {
				return err
			}
			var written *types.WikiPage
			written, writeErr = transactionalWiki.UpdatePage(writeCtx, page)
			if writeErr == nil {
				if written == nil {
					return fmt.Errorf("source Wiki page update returned no persisted page")
				}
				*page = *written
			}
		}
		if writeErr != nil {
			return writeErr
		}
		persistNow := time.Now().UTC()
		if contributionProjection != nil {
			err = contributionProjection.persistInTx(tx, page, attempt, dependencyFileIDs, moduleMemberFileIDs, inventoryComplete, persistNow)
		} else {
			err = repository.PersistSourceWikiPageContributionInTx(
				tx, page, attempt.TopicKind, attempt.TopicKey, dependencyFileIDs, moduleMemberFileIDs, inventoryComplete, persistNow,
			)
		}
		if err != nil {
			return err
		}
		now := time.Now()
		var result *gorm.DB
		if stagedCandidate {
			result = tx.Model(&types.SourceWikiAttempt{}).Where("id = ? AND batch_id = ? AND status = 'staged' AND staged_page_version = ?", attempt.ID, attempt.BatchID, baseVersion).
				Updates(map[string]any{"status": "ready", "reason": "", "updated_at": now})
		} else {
			var running types.SourceWikiAttempt
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='running' AND epoch=? AND lease_owner=? AND lease_expires_at>? AND deadline_at>?", attempt.ID, lease.Epoch, lease.Owner, now, now).Take(&running).Error; err != nil {
				return fmt.Errorf("source Wiki attempt is no longer running")
			}
			result = tx.Model(&types.SourceWikiAttempt{}).Where("id=? AND status='running' AND epoch=? AND lease_owner=?", attempt.ID, lease.Epoch, lease.Owner).
				Updates(map[string]any{"status": "ready", "reason": "", "lease_owner": "", "lease_expires_at": nil, "updated_at": now})
		}
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return repository.ErrSourceWikiAttemptFenced
		}
		if stagedCandidate {
			updated := tx.Model(&types.SourceWikiCoverageTopic{}).
				Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND topic_key = ? AND snapshot_id = ? AND batch_id = ? AND initial = TRUE AND attempt_id = ? AND status = 'draft'",
					attempt.TenantID, attempt.KnowledgeBaseID, attempt.SourceID, attempt.TopicKey, attempt.SnapshotID, attempt.BatchID, attempt.ID).
				Updates(map[string]any{"status": "ready", "last_ready_snapshot_id": attempt.SnapshotID, "reason": "", "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return repository.ErrSourceWikiBatchInvalidState
			}
		}
		if coverageSchemaAvailable && attempt.BatchID == "" && attempt.TopicKind == "module" && attempt.TopicKey == "module/"+attempt.ModulePath {
			if err := tx.Model(&types.SourceWikiCoverageTopic{}).
				Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND topic_key = ? AND snapshot_id = ? AND wiki_slug = ? AND initial = FALSE AND status = 'expansion'",
					attempt.TenantID, attempt.KnowledgeBaseID, attempt.SourceID, attempt.TopicKey, attempt.SnapshotID, attempt.Slug).
				Updates(map[string]any{
					"status": "ready", "attempt_id": attempt.ID,
					"last_ready_snapshot_id": attempt.SnapshotID, "reason": "", "updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		if manualRepairTopic != nil {
			updated := tx.Model(manualRepairTopic).Where(
				"id = ? AND batch_id = ? AND snapshot_id = ? AND wiki_slug = ? AND initial = TRUE AND status IN ('failed', 'insufficient_evidence')",
				manualRepairTopic.ID, *manualRepairTopic.BatchID, attempt.SnapshotID, attempt.Slug,
			).Updates(map[string]any{
				"status": "ready", "attempt_id": attempt.ID,
				"last_ready_snapshot_id": attempt.SnapshotID, "reason": "", "updated_at": now,
			})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return repository.ErrSourceWikiBatchInvalidState
			}
		}
		if err := repository.ReleaseSourceWikiAttemptEvidence(tx, attempt.ID); err != nil {
			return err
		}
		attempt.Status, attempt.Reason, attempt.UpdatedAt = "ready", "", now
		return nil
	})
}

func (s *sourceWikiService) ListAttempts(ctx context.Context, kbID string) ([]*types.SourceWikiAttempt, error) {
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kbID}})
	if err != nil {
		return nil, err
	}
	defer release()
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, kbID)
	if err != nil {
		return nil, err
	}
	attempts := make([]*types.SourceWikiAttempt, 0)
	permission := source.SourcePermissionSQL(ctx, "source_wiki_attempts.source_id", "NULL")
	files := "(CASE WHEN jsonb_typeof(evidence_knowledge_ids)='array' THEN evidence_knowledge_ids ELSE '[]'::jsonb END)"
	completeFiles := "jsonb_array_length(" + files + ")>0 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(" + files + ") af WHERE NOT (" + source.SourcePermissionSQL(ctx, "source_wiki_attempts.source_id", "af") + "))"
	if err = s.db.WithContext(ctx).Where("knowledge_base_id=? AND tenant_id=?", kbID, kb.TenantID).Where("(" + permission + ") OR (" + completeFiles + ")").Order("created_at DESC").Limit(50).Find(&attempts).Error; err != nil {
		return nil, err
	}
	// Draft prose is available only to authorized editors; the user-facing
	// status view exposes reasons and budget counters, never unverified prose.
	for _, a := range attempts {
		a.Draft = nil
	}
	return attempts, nil
}

func (s *sourceWikiService) ReadEvidence(ctx context.Context, kbID, slug string, version int, id string) (*types.SourceFileView, error) {
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kbID}})
	if err != nil {
		return nil, err
	}
	defer release()
	var pageID string
	var revisionID *string
	var provenance *types.SourceWikiProvenance
	var ownerVersion int

	if version > 0 {
		rev, revErr := s.wiki.GetRevision(ctx, kbID, slug, version)
		if revErr == nil {
			pageID, revisionID, provenance, ownerVersion = rev.PageID, &rev.ID, rev.SourceProvenance, rev.Version
		} else if !errors.Is(revErr, repository.ErrWikiPageNotFound) {
			return nil, revErr
		}
	}
	if provenance == nil {
		current, readErr := s.wiki.GetPageBySlug(ctx, kbID, slug)
		if readErr != nil {
			return nil, readErr
		}
		if version > 0 && version != current.Version {
			return nil, repository.ErrWikiPageNotFound
		}
		pageID, provenance, ownerVersion = current.ID, current.SourceProvenance, current.Version
	}

	if provenance == nil {
		return nil, fmt.Errorf("page has no registered source evidence")
	}
	for _, e := range provenance.Evidence {
		if e.ID == id {
			file, err := repository.ReadSourceWikiEvidence(ctx, s.db, pageID, revisionID, ownerVersion, e)
			if err != nil {
				return nil, err
			}
			if err = source.ValidateReadScope(ctx); err != nil {
				return nil, err
			}
			if err = repository.ValidateSourceWikiEvidencePermission(ctx, s.db, e); err != nil {
				return nil, err
			}
			return file, nil
		}
	}
	return nil, fmt.Errorf("evidence is not registered to this body")
}

// sourceWikiSameModel compares the actual provider client configuration,
// including credentials, and a durable update timestamp to reject ABA edits.
func sourceWikiSameModel(a, b *types.Model) bool {
	if a == nil || b == nil || a.ID != b.ID || a.TenantID != b.TenantID || a.Name != b.Name || a.Type != b.Type || a.Source != b.Source || a.Status != b.Status || !b.DeletedAt.Time.IsZero() || !a.UpdatedAt.Equal(b.UpdatedAt) {
		return false
	}
	ap, _ := json.Marshal(a.Parameters)
	bp, _ := json.Marshal(b.Parameters)
	return bytes.Equal(ap, bp)
}
