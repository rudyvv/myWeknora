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
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sourceWikiMaxEvidenceBytes = 32768
	sourceWikiMaxFiles         = 16
)

type sourceWikiService struct {
	wiki      interfaces.WikiPageService
	kb        interfaces.KnowledgeBaseService
	knowledge interfaces.KnowledgeService
	models    interfaces.ModelService
	db        *gorm.DB
}

func NewSourceWikiService(wiki interfaces.WikiPageService, kb interfaces.KnowledgeBaseService, knowledge interfaces.KnowledgeService, models interfaces.ModelService, db *gorm.DB) interfaces.SourceWikiService {
	return &sourceWikiService{wiki: wiki, kb: kb, knowledge: knowledge, models: models, db: db}
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
	var members []types.SourceSnapshotMember
	q := s.db.WithContext(ctx).Table("source_snapshot_members sm").Select("sm.*").
		Joins("JOIN source_snapshots ss ON ss.id=sm.snapshot_id AND ss.data_source_id=? AND ss.knowledge_base_id=? AND ss.state='published'", sourceID, kbID).
		Where("sm.status='parsed' AND NOT sm.generated").
		Where(source.SnapshotSQL(ctx, "sm.snapshot_id", "ss.data_source_id", "sm.source_file_id"))
	if module != "." {
		q = q.Where("sm.path LIKE ? ESCAPE '!'", strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(module)+"/%")
	}
	if err := q.Order("sm.path").Limit(sourceWikiMaxFiles + 1).Find(&members).Error; err != nil {
		return nil, err
	}
	if len(members) > sourceWikiMaxFiles {
		return nil, fmt.Errorf("module exceeds %d files; choose a smaller module", sourceWikiMaxFiles)
	}
	var result []collectedWikiEvidence
	total := 0
	for _, member := range members {
		file, err := s.knowledge.GetSourceFile(ctx, member.SourceFileID, member.FileVersionID)
		if err != nil {
			return nil, fmt.Errorf("fixed module evidence cannot be read")
		}
		if file.FileVersionID != member.FileVersionID || file.SnapshotID != member.SnapshotID || file.DataSourceID != sourceID {
			return nil, fmt.Errorf("fixed module evidence changed")
		}
		// The first-card collector accepts complete small files as separately
		// bounded evidence. It never truncates a file while claiming a full range.
		if len(file.RawContent) > sourceWikiMaxEvidenceBytes || total+len(file.RawContent) > sourceWikiMaxEvidenceBytes {
			return nil, fmt.Errorf("module evidence exceeds byte budget; choose a smaller module")
		}
		if len(file.RawContent) == 0 {
			continue
		}
		total += len(file.RawContent)
		hash := sha256.Sum256(file.RawContent)
		e := types.SourceWikiEvidence{ID: fmt.Sprintf("e%03d", len(result)+1), KnowledgeID: file.KnowledgeID, SHA256: file.SHA256, TextSHA256: hex.EncodeToString(hash[:]), SourceEvidence: types.SourceEvidence{DataSourceID: file.DataSourceID, SnapshotID: file.SnapshotID, FileVersionID: file.FileVersionID, ProjectID: file.ProjectID, CommitSHA: file.CommitSHA, Path: file.Path, Quality: file.Quality, Range: types.SourceRange{StartByte: 0, EndByte: len(file.RawContent), StartLine: 1, EndLine: 1 + bytes.Count(file.RawContent[:len(file.RawContent)-1], []byte("\n"))}}}
		e.GitLabURL = source.GitLabBlobURL(file.RepositoryURL, e.CommitSHA, e.Path, e.Range)
		result = append(result, collectedWikiEvidence{Evidence: e, Text: file.Content})
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

func (s *sourceWikiService) publishCard(ctx context.Context, kb *types.KnowledgeBase, sourceID, snapshotID string, baseVersion int, existing, page *types.WikiPage, expectedSource *types.DataSource, expectedModel *types.Model, attempt *types.SourceWikiAttempt, lease types.SourceWikiAttemptLease) error {
	if err := source.ValidateReadScope(ctx); err != nil {
		return err
	}
	// The publication lock serializes the final card write with source publish;
	// an old model result cannot become a current ready page after a new SHA.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		var writeErr error
		if existing == nil {
			_, writeErr = transactionalWiki.CreatePage(writeCtx, page)
		} else {
			page.ID = existing.ID
			page.Version = baseVersion
			page.FolderID = existing.FolderID
			_, writeErr = transactionalWiki.UpdatePage(writeCtx, page)
		}
		if writeErr != nil {
			return writeErr
		}
		now := time.Now()
		var running types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status='running' AND epoch=? AND lease_owner=? AND lease_expires_at>? AND deadline_at>?", attempt.ID, lease.Epoch, lease.Owner, now, now).Take(&running).Error; err != nil {
			return fmt.Errorf("source Wiki attempt is no longer running")
		}
		result := tx.Model(&types.SourceWikiAttempt{}).Where("id=? AND status='running' AND epoch=? AND lease_owner=?", attempt.ID, lease.Epoch, lease.Owner).
			Updates(map[string]any{"status": "ready", "reason": "", "lease_owner": "", "lease_expires_at": nil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return repository.ErrSourceWikiAttemptFenced
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
