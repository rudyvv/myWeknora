//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"strings"
	"sync"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	gormlogger "gorm.io/gorm/logger"
)

func syncSourceFixture(t *testing.T, f *javaSourceFixture, sourceIDs ...string) {
	t.Helper()
	sourceID := f.ds.ID
	if len(sourceIDs) > 0 {
		sourceID = sourceIDs[0]
	}
	log, err := f.service.ManualSync(f.ctx, sourceID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: sourceID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
}

func TestSourceAndOrdinaryDocumentsMixWithoutWideningRepositoryPrompt(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Other.java": []byte("class Other { String getPushSchedule() { return \"unselected tagged source\"; } }\n")})
	syncSourceFixture(t, f)
	document := &types.Knowledge{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Type: "file", Title: "ordinary-handbook-outside-repository", FileName: "handbook.md", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, f.db.Create(document).Error)
	chunk := &types.Chunk{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, KnowledgeID: document.ID, Content: "getPushSchedule ordinary document text", ChunkType: types.ChunkTypeText, IsEnabled: true, IndexStatus: "ready"}
	require.NoError(t, repository.NewChunkRepository(f.db).CreateChunks(f.ctx, []*types.Chunk{chunk}))
	require.NoError(t, f.db.Exec(`INSERT INTO embeddings(source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,content,dimension,embedding,is_enabled) VALUES (?,1,?,?,?,?,3,'[1,0,0]',true)`, chunk.ID, chunk.ID, document.ID, f.kb.ID, chunk.Content).Error)
	for _, keywordOnly := range []bool{true, false} {
		params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly}
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.Len(t, hits, 3)
		params.SourceIDs = []string{f.ds.ID}
		hits, err = f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.Len(t, hits, 2)
		require.NotEqual(t, document.ID, hits[0].KnowledgeID)
	}
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	model := &sourceQuestionModel{}
	engine, err := sourceFixtureAgent(t, f, model, targets)
	require.NoError(t, err)
	_, err = engine.Execute(f.ctx, "source-question", "source-answer", "Read the schedule method", nil)
	require.NoError(t, err)
	for _, messages := range model.calls {
		for _, message := range messages {
			require.NotContains(t, message.Content, document.Title, "repository-only scope must also constrain the bound-KB directory")
		}
	}
	// An explicit ordinary-file target can be combined with a repository target.
	mixed := append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, TenantID: 1, KnowledgeIDs: []string{document.ID}})
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, mixed)
	require.NoError(t, err)
	defer release()
	rows, err := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, rows, 3)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "document and unselected source"}
	require.NoError(t, f.db.Create(tag).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, document.ID, []string{tag.ID}))
	require.NoError(t, f.db.Exec("UPDATE embeddings SET tag_id=? WHERE knowledge_id=?", tag.ID, document.ID).Error)
	var selectedFile string
	for _, row := range rows {
		if row.Metadata["source_path"] == "src/Other.java" {
			require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, row.KnowledgeID, []string{tag.ID}))
		} else if row.Metadata["source_path"] == "src/Service.java" {
			selectedFile = row.KnowledgeID
		}
	}
	require.NotEmpty(t, selectedFile)
	intersection := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, TenantID: 1, KnowledgeIDs: []string{document.ID, selectedFile}, TagIDs: []string{tag.ID}}}
	intersectionCtx, intersectionRelease, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, intersection)
	require.NoError(t, err)
	defer intersectionRelease()
	onlyDocument := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, TenantID: 1, KnowledgeIDs: []string{document.ID}}}
	documentCtx, documentRelease, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, onlyDocument)
	require.NoError(t, err)
	defer documentRelease()
	require.NoError(t, f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error)
	rows, err = f.kbs.HybridSearch(documentCtx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1})
	require.NoError(t, err, "clearing an unrelated repository must not interrupt an ordinary-file question")
	require.Len(t, rows, 1)
	require.Equal(t, document.ID, rows[0].KnowledgeID)
	rows, err = f.kbs.HybridSearch(intersectionCtx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1, KnowledgeIDs: []string{document.ID, selectedFile}, TagIDs: []string{tag.ID}})
	require.NoError(t, err, "file and tag must match the same source member before retaining a repository")
	require.Len(t, rows, 1)
	require.Equal(t, document.ID, rows[0].KnowledgeID)
}

func TestSourceAwareSearchPreservesOrdinaryFAQEntryTags(t *testing.T) {
	f := newJavaSourceFixture(t)
	kb := &types.KnowledgeBase{ID: uuid.NewString(), TenantID: 1, Name: "ordinary FAQ", Type: "faq", EmbeddingModelID: f.kb.EmbeddingModelID, IndexingStrategy: types.IndexingStrategy{VectorEnabled: true}}
	require.NoError(t, f.db.Create(kb).Error)
	knowledge := &types.Knowledge{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: kb.ID, Type: "faq", Title: "ordinary FAQ entries", ParseStatus: types.ParseStatusCompleted}
	require.NoError(t, f.db.Create(knowledge).Error)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: kb.ID, Name: "selected FAQ"}
	require.NoError(t, f.db.Create(tag).Error)
	var chunks []*types.Chunk
	for i, answer := range []string{"selected answer", "outside FAQ answer"} {
		metadata, err := json.Marshal(types.FAQChunkMetadata{StandardQuestion: "getPushSchedule", Answers: []string{answer}})
		require.NoError(t, err)
		chunk := &types.Chunk{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: kb.ID, KnowledgeID: knowledge.ID, ChunkIndex: i, ChunkType: types.ChunkTypeFAQ, Content: "getPushSchedule", Metadata: metadata, IsEnabled: true, IndexStatus: "ready"}
		if i == 0 {
			chunk.TagID = tag.ID
		}
		chunks = append(chunks, chunk)
	}
	chunks[0].NextChunkID = chunks[1].ID
	chunks[0].ParentChunkID = chunks[1].ID
	chunks[0].RelationChunks, _ = json.Marshal([]string{chunks[1].ID})
	require.NoError(t, repository.NewChunkRepository(f.db).CreateChunks(f.ctx, chunks))
	for _, chunk := range chunks {
		require.NoError(t, f.db.Exec(`INSERT INTO embeddings(source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,tag_id,content,dimension,embedding,is_enabled) VALUES (?,1,?,?,?,?,?,3,'[1,0,0]',true)`, chunk.ID, chunk.ID, knowledge.ID, kb.ID, chunk.TagID, chunk.Content).Error)
	}
	rows, err := f.kbs.HybridSearch(f.ctx, kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, DisableKeywordsMatch: true, TagIDs: []string{tag.ID}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, chunks[0].ID, rows[0].ID)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb.ID, TenantID: 1, TagIDs: []string{tag.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	info, err := f.knowledge.GetKnowledgeByIDOnly(ctx, knowledge.ID)
	require.NoError(t, err)
	require.Equal(t, knowledge.ID, info.ID)
	page, err := f.chunks.ListPagedChunksByKnowledgeID(ctx, knowledge.ID, &types.Pagination{Page: 1, PageSize: 10}, []types.ChunkType{types.ChunkTypeFAQ})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	items := page.Data.([]*types.Chunk)
	require.Len(t, items, 1)
	require.Equal(t, chunks[0].ID, items[0].ID)
}

func TestSourceSharedQuestionRevocationAndPurgeOverridePinnedReads(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	foreign := types.WithCaller(context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2)), types.Caller{TenantID: 2, UserID: "viewer", Role: types.TenantRoleViewer})
	_, err = f.chunks.GetChunkByIDOnly(foreign, hits[0].ID)
	require.Error(t, err, "a chunk handle alone cannot authorize a source read")
	require.NoError(t, f.db.Create(&types.User{ID: "owner", Username: "owner", Email: "owner@example.invalid", TenantID: 1}).Error)
	org := &types.Organization{ID: uuid.NewString(), Name: "source sharing", OwnerID: "owner", OwnerTenantID: 1, InviteCode: "source-sharing-fixture"}
	require.NoError(t, f.db.Create(org).Error)
	for _, member := range []*types.OrganizationTenantMember{{ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 1, Role: types.OrgRoleAdmin, RepresentativeUserID: "owner"}, {ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 2, Role: types.OrgRoleViewer, RepresentativeUserID: "owner"}} {
		require.NoError(t, f.db.Create(member).Error)
	}
	share, err := f.shares.ShareKnowledgeBase(f.ctx, f.kb.ID, org.ID, "owner", 1, types.OrgRoleViewer)
	require.NoError(t, err)
	grant, err := access.ResolveKB(foreign, access.KBRequest{Caller: types.CallerFromContext(foreign)}, f.kb, types.OrgRoleViewer, f.shares, nil)
	require.NoError(t, err)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(grant.Context(foreign), targets)
	require.NoError(t, err)
	defer release()
	view, err := f.knowledge.GetSourceFile(ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, f.sha, view.CommitSHA)
	result, err := agenttools.NewSourceAwareGrepChunksTool(f.db, targets).Execute(ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.Equal(t, 1, result.Data["result_count"])
	require.NoError(t, f.shares.RemoveShare(f.ctx, share.ID, "owner", 1))
	_, err = f.knowledge.GetSourceFile(ctx, hits[0].KnowledgeID)
	require.Error(t, err)
	_, err = f.chunks.GetChunkByIDOnly(ctx, hits[0].ID)
	require.Error(t, err)
	_, err = agenttools.NewSourceAwareGrepChunksTool(f.db, targets).Execute(ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.Error(t, err)
	_, err = f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
	ownerCtx, ownerRelease, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer ownerRelease()
	// Explicit purge invalidation is injected at the durable revocation boundary;
	// the dedicated purge workflow is implemented by T19.
	require.NoError(t, f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error)
	_, err = f.knowledge.GetSourceFile(ownerCtx, hits[0].KnowledgeID)
	require.Error(t, err)
	_, err = f.kbs.HybridSearch(ownerCtx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
}

// This fixture replaces the external generation model only. Agent services,
// tools, source parsing, manifest publication and both indexes are real.
type sourceQuestionModel struct {
	calls       [][]chat.Message
	beforeReply func(int)
	beforeClose func(int)
	answerSeen  chan struct{}
}

func (m *sourceQuestionModel) GetModelName() string { return "source-question-fixture" }
func (m *sourceQuestionModel) GetModelID() string   { return "source-question-fixture" }
func (m *sourceQuestionModel) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("unexpected non-streaming model call")
}
func (m *sourceQuestionModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	step := len(m.calls)
	m.calls = append(m.calls, append([]chat.Message(nil), messages...))
	if m.beforeReply != nil {
		m.beforeReply(step)
	}
	response := types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "stop", Content: `fixed source question completed <ref id="c1"/>`}
	tools := []struct{ name, args string }{{agenttools.ToolGrepChunks, `{"query":"getPushSchedule"}`}, {agenttools.ToolGetDocumentInfo, `{"knowledge_ids":["d1"]}`}, {agenttools.ToolListKnowledgeChunks, `{"knowledge_id":"d1","limit":1,"offset":0}`}}
	if step < len(tools) {
		response.Content = ""
		response.FinishReason = "tool_calls"
		response.ToolCalls = []types.LLMToolCall{{ID: fmt.Sprintf("source-tool-%d", step), Function: types.FunctionCall{Name: tools[step].name, Arguments: tools[step].args}}}
	}
	if step == 3 && m.beforeClose != nil {
		response.Done = false
		response.Content = `fixed source question completed <ref id="c`
	}
	ch := make(chan types.StreamResponse)
	go func() {
		defer close(ch)
		select {
		case ch <- response:
		case <-ctx.Done():
			return
		}
		if m.beforeClose != nil {
			m.beforeClose(step)
		}
	}()
	return ch, nil
}

func sourceFixtureAgent(t *testing.T, f *javaSourceFixture, model *sourceQuestionModel, targets types.SearchTargets) (interfaces.AgentEngine, error) {
	t.Helper()
	cfg := &config.Config{Conversation: &config.ConversationConfig{}, KnowledgeBase: &config.KnowledgeBaseConfig{}}
	service := NewAgentService(cfg, nil, f.kbs.(interfaces.KnowledgeBaseService), f.knowledge, nil, f.chunks, nil, nil, event.NewEventBus(), f.db, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	bus := event.NewEventBus()
	if model.answerSeen != nil {
		var once sync.Once
		bus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
			if data, ok := evt.Data.(event.AgentFinalAnswerData); ok && strings.Contains(data.Content, "fixed source question completed") {
				once.Do(func() { close(model.answerSeen) })
			}
			return nil
		})
	}
	return service.CreateAgentEngine(f.ctx, &types.AgentConfig{MaxIterations: 6, KnowledgeBases: []string{f.kb.ID}, SearchTargets: targets, AllowedTools: []string{agenttools.ToolGrepChunks, agenttools.ToolGetDocumentInfo, agenttools.ToolListKnowledgeChunks}, SystemPrompt: "Use the available knowledge tools to answer.", UseCustomSystemPrompt: true}, model, nil, bus, "source-question", "source-answer")
}

func TestSourceAgentQuestionPinsBeforeFirstToolAndStopsAfterPurge(t *testing.T) {
	for _, purge := range []bool{false, true} {
		t.Run(fmt.Sprintf("purge=%v", purge), func(t *testing.T) {
			f := newJavaSourceFixture(t)
			syncSourceFixture(t, f)
			targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
			model := &sourceQuestionModel{}
			engine, err := sourceFixtureAgent(t, f, model, targets)
			require.NoError(t, err)
			f.advanceJava("class Service { String getPushSchedule() { return \"new publication\"; } }\n")
			syncSourceFixture(t, f)
			if purge {
				model.beforeReply = func(step int) {
					if step == 3 {
						require.NoError(t, f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error)
					}
				}
			}
			state, err := engine.Execute(f.ctx, "source-question", "source-answer", "Read the schedule method and its provenance", nil)
			if purge {
				require.Error(t, err)
				if state != nil {
					require.NotContains(t, state.FinalAnswer, "fixed source question completed")
				}
			} else {
				require.NoError(t, err)
				require.Contains(t, state.FinalAnswer, "fixed source question completed")
				require.Contains(t, state.FinalAnswer, `commit_sha="`+f.sha+`"`)
				require.Contains(t, state.FinalAnswer, `source_id="`+f.ds.ID+`"`)
				require.Contains(t, state.FinalAnswer, `path="src/Service.java"`)
			}
			require.Len(t, model.calls, 4)
			var modelInput strings.Builder
			for _, messages := range model.calls {
				for _, message := range messages {
					if message.Role == "tool" {
						modelInput.WriteString(message.Content)
					}
				}
			}
			require.Contains(t, modelInput.String(), "预约")
			require.Contains(t, modelInput.String(), f.sha)
			require.NotContains(t, modelInput.String(), "new publication")
			fresh, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
			require.NoError(t, err)
			if purge {
				require.Empty(t, fresh)
			} else {
				require.Len(t, fresh, 1)
				require.Contains(t, fresh[0].Content, "new publication")
			}
		})
	}
}

func TestSourceAgentQuestionRejectsPurgeAtProviderClose(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	model := &sourceQuestionModel{answerSeen: make(chan struct{})}
	model.beforeClose = func(step int) {
		if step != 3 {
			return
		}
		select {
		case <-model.answerSeen:
		case <-time.After(10 * time.Second):
			t.Error("Agent did not emit the final frame before close")
			return
		}
		if err := f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error; err != nil {
			t.Error(err)
		}
	}
	engine, err := sourceFixtureAgent(t, f, model, targets)
	require.NoError(t, err)
	state, err := engine.Execute(f.ctx, "source-question", "source-answer", "Read the schedule method", nil)
	require.Error(t, err)
	if state != nil {
		require.False(t, state.IsComplete)
		require.Empty(t, state.FinalAnswer)
	}
	require.Len(t, model.calls, 4, "revocation must also prevent a synthesis fallback model call")
}

func TestSourceQuestionIntersectsRepositoryFileAndTagsForAgentReads(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Other.java": []byte("class Other { String getPushSchedule() { return \"scope outsider\"; } }\n")})
	syncSourceFixture(t, f)
	second := &types.DataSource{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "same paths in another source", Type: f.ds.Type, Status: types.DataSourceStatusPaused, Config: append(types.JSON{}, f.ds.Config...)}
	_, err := f.service.CreateDataSource(f.ctx, second)
	require.NoError(t, err)
	syncSourceFixture(t, f, second.ID)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, hits, 4)
	allTargets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}}
	all, err := agenttools.NewSourceAwareGrepChunksTool(f.db, allTargets).Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.Equal(t, 4, all.Data["result_count"], "same code in independent sources must not collapse to one file")
	var selected *types.SearchResult
	for _, hit := range hits {
		if hit.Metadata["datasource_id"] == f.ds.ID && hit.Metadata["source_path"] == "src/Service.java" {
			selected = hit
		}
	}
	require.NotNil(t, selected)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "selected source file"}
	require.NoError(t, f.db.Create(tag).Error)
	// The tag deliberately includes outsiders. A file selection must intersect it.
	for _, hit := range hits {
		require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, hit.KnowledgeID, []string{tag.ID}))
	}
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{selected.KnowledgeID}, TagIDs: []string{tag.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	grep := agenttools.NewSourceAwareGrepChunksTool(f.db, targets)
	// Fault-injected stale links must not turn context completion into a
	// backdoor to another file or repository.
	var outsideIDs []string
	for _, hit := range hits {
		if hit.ID != selected.ID {
			outsideIDs = append(outsideIDs, hit.ID)
		}
	}
	links, err := json.Marshal(outsideIDs)
	require.NoError(t, err)
	require.NoError(t, f.db.Exec("UPDATE chunks SET parent_chunk_id=?,pre_chunk_id=?,next_chunk_id=?,relation_chunks=?::jsonb WHERE id=?", outsideIDs[0], outsideIDs[1], outsideIDs[2], string(links), selected.ID).Error)
	for _, keywordOnly := range []bool{true, false} {
		rows, searchErr := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, searchErr)
		require.Len(t, rows, 1)
		require.Equal(t, selected.ID, rows[0].ID)
		require.NotContains(t, rows[0].Content, "scope outsider")
	}
	result, err := grep.Execute(ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.Equal(t, 1, result.Data["result_count"])
	for _, hit := range hits {
		list := agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, targets)
		args, _ := json.Marshal(map[string]any{"chunk_id": hit.ID})
		read, err := list.Execute(ctx, args)
		if hit.ID == selected.ID {
			require.NoError(t, err)
			require.True(t, read.Success)
		} else {
			require.Error(t, err)
			require.NotContains(t, read.Output, hit.Content)
		}
		file, err := f.knowledge.GetSourceFile(ctx, hit.KnowledgeID)
		if hit.ID == selected.ID {
			require.NoError(t, err)
			require.Equal(t, f.ds.ID, file.DataSourceID)
		} else {
			require.Error(t, err)
		}
	}
	// Even a direct tool call without a pre-pinned context must honor SourceIDs.
	otherTargets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{second.ID}}}
	args, _ := json.Marshal(map[string]any{"chunk_id": selected.ID})
	denied, err := agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, otherTargets).Execute(f.ctx, args)
	require.Error(t, err)
	require.False(t, denied.Success)
	for _, sourceID := range []string{f.ds.ID, second.ID, uuid.NewString()} {
		scopes := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{sourceID}}}
		found, err := agenttools.NewSourceAwareGrepChunksTool(f.db, scopes).Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
		require.NoError(t, err)
		expected := 2
		if sourceID != f.ds.ID && sourceID != second.ID {
			expected = 0
		}
		require.Equal(t, expected, found.Data["result_count"])
	}
}

func TestSourceQuestionRetainsPublishedSnapshotAcrossPublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	reader, ok := f.kbs.(interfaces.SourceReadService)
	require.True(t, ok, "public KB service must expose question-scoped source reading")
	ctx, release, err := reader.BeginSourceRead(f.ctx, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}})
	require.NoError(t, err)
	defer release()
	params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10}
	before, err := f.kbs.HybridSearch(ctx, f.kb.ID, params)
	require.NoError(t, err)
	require.NotEmpty(t, before)
	file, err := f.knowledge.GetSourceFile(ctx, before[0].KnowledgeID)
	require.NoError(t, err)
	newSHA := f.advanceJava("package demo;\npublic class Service {\n public String getPushSchedule(String name) { return \"new publication\"; }\n}\n")
	syncSourceFixture(t, f)
	for _, question := range []struct {
		ctx       context.Context
		sha, body string
	}{
		{ctx, f.sha, "预约"}, {f.ctx, newSHA, "new publication"},
	} {
		for _, keywordOnly := range []bool{true, false} {
			query := params
			query.DisableVectorMatch, query.DisableKeywordsMatch = keywordOnly, !keywordOnly
			hits, err := f.kbs.HybridSearch(question.ctx, f.kb.ID, query)
			require.NoError(t, err)
			require.Len(t, hits, 1)
			require.Contains(t, hits[0].Content, question.body)
			require.Equal(t, question.sha, hits[0].Metadata["commit_sha"])
		}
		view, err := f.knowledge.GetSourceFile(question.ctx, file.KnowledgeID)
		require.NoError(t, err)
		require.Equal(t, question.sha, view.CommitSHA)
		require.Contains(t, view.Content, question.body)
		info, err := f.knowledge.GetKnowledgeByIDOnly(question.ctx, file.KnowledgeID)
		require.NoError(t, err)
		require.Equal(t, question.sha, info.GetMetadata()["commit_sha"])
		require.Equal(t, view.SHA256, info.FileHash)
		batch, err := f.knowledge.GetKnowledgeBatch(question.ctx, 1, []string{file.KnowledgeID})
		require.NoError(t, err)
		require.Len(t, batch, 1)
		require.Equal(t, question.sha, batch[0].GetMetadata()["commit_sha"])
		targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}}
		for _, operation := range []struct {
			tool types.Tool
			args any
		}{
			{agenttools.NewSourceAwareGrepChunksTool(f.db, targets), map[string]any{"query": "getPushSchedule"}},
			{agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, targets), map[string]any{"knowledge_id": file.KnowledgeID, "limit": 1, "offset": 0}},
			{agenttools.NewWikiReadSourceDocTool(f.knowledge, f.chunks, targets), map[string]any{"knowledge_id": file.KnowledgeID, "query": "getPushSchedule"}},
		} {
			args, err := json.Marshal(operation.args)
			require.NoError(t, err)
			result, err := operation.tool.Execute(question.ctx, args)
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			require.Contains(t, result.Output, question.body)
		}
	}
	release()
	_, err = f.knowledge.GetSourceFile(ctx, file.KnowledgeID)
	require.Error(t, err, "released question must not silently switch to the latest source")
}

// The SQL observer is used only at the spec-approved query-plan seam. Results
// are asserted through public HybridSearch, with real keyword/vector indexes.
type sourceQueryPlanObserver struct {
	gormlogger.Interface
	mu      sync.Mutex
	queries []string
}

func (o *sourceQueryPlanObserver) LogMode(gormlogger.LogLevel) gormlogger.Interface { return o }
func (o *sourceQueryPlanObserver) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	lower := strings.ToLower(sql)
	if strings.Contains(lower, "source_read_scopes") && strings.Contains(lower, "limit") && (strings.Contains(lower, "paradedb.score") || strings.Contains(lower, "<=>")) {
		o.mu.Lock()
		o.queries = append(o.queries, sql)
		o.mu.Unlock()
	}
	if err != nil {
		o.Interface.Trace(ctx, begin, fc, err)
	}
}

func TestSourceScopesApplyBeforeTopKInRealIndexQueryPlans(t *testing.T) {
	files := make(map[string][]byte)
	for i := 0; i < 80; i++ {
		files[fmt.Sprintf("src/Outside%d.java", i)] = []byte(fmt.Sprintf("class Outside%d { String getPushSchedule() { return \"getPushSchedule getPushSchedule getPushSchedule getPushSchedule\"; } }\n", i))
	}
	f := newJavaSourceFixture(t, files)
	syncSourceFixture(t, f)
	second := &types.DataSource{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "independent repository", Type: f.ds.Type, Status: types.DataSourceStatusPaused, Config: append(types.JSON{}, f.ds.Config...)}
	_, err := f.service.CreateDataSource(f.ctx, second)
	require.NoError(t, err)
	syncSourceFixture(t, f, second.ID)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 500, DisableVectorMatch: true})
	require.NoError(t, err)
	require.Len(t, hits, 162)
	var selected *types.SearchResult
	for _, hit := range hits {
		if hit.Metadata["datasource_id"] == f.ds.ID && hit.Metadata["source_path"] == "src/Service.java" {
			selected = hit
		}
	}
	require.NotNil(t, selected)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "selected file"}
	require.NoError(t, f.db.Create(tag).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, selected.KnowledgeID, []string{tag.ID}))
	// Higher-similarity outsiders exceed the vector candidate budget of 100.
	require.NoError(t, f.db.Exec("UPDATE embeddings SET embedding='[1,0.1,0]' WHERE chunk_id=?", selected.ID).Error)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{selected.KnowledgeID}, TagIDs: []string{tag.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	observer := &sourceQueryPlanObserver{Interface: f.db.Config.Logger}
	f.db.Config.Logger = observer
	for _, keywordOnly := range []bool{true, false} {
		rows, searchErr := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{selected.KnowledgeID}, TagIDs: []string{tag.ID}})
		require.NoError(t, searchErr)
		t.Logf("keywordOnly=%v rows=%d queries=%v", keywordOnly, len(rows), observer.queries)
		require.Len(t, rows, 1, "keywordOnly=%v", keywordOnly)
		require.Equal(t, selected.ID, rows[0].ID)
	}
	require.Len(t, observer.queries, 2)
	for _, sql := range append([]string(nil), observer.queries...) {
		// The relational filters are inside the candidate query's first LIMIT.
		prefix := strings.SplitN(strings.ToLower(sql), "limit", 2)[0]
		for _, required := range []string{"source_read_scopes", "source_chunk_references", "source_snapshot_members", "knowledge_tag_relations", strings.ToLower(f.kb.ID), strings.ToLower(selected.KnowledgeID), strings.ToLower(f.ds.ID)} {
			require.Contains(t, prefix, required)
		}
		var planJSON string
		require.NoError(t, f.db.Raw("EXPLAIN (ANALYZE, FORMAT JSON) "+sql).Scan(&planJSON).Error)
		var plans []struct {
			Plan map[string]interface{} `json:"Plan"`
		}
		require.NoError(t, json.Unmarshal([]byte(planJSON), &plans))
		require.Len(t, plans, 1)
		require.Equal(t, float64(1), plans[0].Plan["Actual Rows"])
		require.Contains(t, planJSON, "Limit")
		require.Contains(t, planJSON, "source_read_scopes")
		t.Logf("public retrieval query plan: %s", planJSON)
	}
	// A current tag removal overrides the file whitelist for every tool/read.
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, selected.KnowledgeID, nil))
	rows, err := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1})
	require.NoError(t, err)
	require.Empty(t, rows)
	_, err = f.knowledge.GetSourceFile(ctx, selected.KnowledgeID)
	require.Error(t, err)
}
