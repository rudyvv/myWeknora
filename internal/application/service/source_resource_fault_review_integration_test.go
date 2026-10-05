//go:build integration

package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestSourceResourceWriteFaultKeepsPublishedResourcesReadableAndRetries(t *testing.T) {
	initialService := []byte(`package demo; public class Service { public String diskFaultOldMarker() { return "diskFaultOldMarker"; } }` + "\n")
	otherSource := []byte(`package demo; public class Other { public String diskFaultOtherMarker() { return "diskFaultOtherMarker"; } }` + "\n")
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/Service.java": initialService,
		"src/Other.java":   otherSource,
	})
	ensureT19LifecycleSchema(t, f)
	syncSourceFixture(t, f)
	publishedCommit := f.sha
	oldHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "diskFaultOldMarker")
	require.Len(t, oldHits, 1)
	oldHandle := oldHits[0].KnowledgeID
	oldView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err)
	require.Equal(t, publishedCommit, oldView.CommitSHA)
	require.Contains(t, oldView.Content, "diskFaultOldMarker")

	other := *f.ds
	other.ID, other.Name = uuid.NewString(), "independent fault-isolation source"
	sourceResourceRegressionSetPaths(t, &other, []string{"src/Other.java"})
	_, err = f.service.CreateDataSource(f.ctx, &other)
	require.NoError(t, err)
	syncSourceFixture(t, f, other.ID)
	otherHits := sourceResourceRegressionSearch(t, f, other.ID, "diskFaultOtherMarker")
	require.Len(t, otherHits, 1)
	otherHandle := otherHits[0].KnowledgeID
	require.NotEqual(t, oldHandle, otherHandle, "the same repository path must have source-local file identities")
	otherView, err := f.knowledge.GetSourceFile(f.ctx, otherHandle)
	require.NoError(t, err)
	require.Equal(t, publishedCommit, otherView.CommitSHA)
	require.Contains(t, otherView.Content, "diskFaultOtherMarker")

	document := &types.Knowledge{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		Type: "file", Title: "ordinary-handbook", FileName: "handbook.md", ParseStatus: types.ParseStatusCompleted,
	}
	require.NoError(t, f.db.Create(document).Error)
	documentChunk := &types.Chunk{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		KnowledgeID: document.ID, Content: "diskFaultOrdinaryDocumentMarker", ChunkType: types.ChunkTypeText,
		IsEnabled: true, IndexStatus: "ready",
	}
	require.NoError(t, repository.NewChunkRepository(f.db).CreateChunks(f.ctx, []*types.Chunk{documentChunk}))
	require.NoError(t, f.db.Exec(`INSERT INTO embeddings
		(source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,content,dimension,embedding,is_enabled)
		VALUES (?,1,?,?,?,?,3,'[1,0,0]',true)`,
		documentChunk.ID, documentChunk.ID, document.ID, f.kb.ID, documentChunk.Content).Error)

	delivered := make(chan *asynq.Task, 2)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: delivered}
	newCommit := f.advanceJava(`package demo; public class Service { public String diskFaultNewMarker() { return "diskFaultNewMarker"; } }` + "\n")
	require.NotEqual(t, publishedCommit, newCommit)
	failedLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NotEmpty(t, failedLog.ID)
	var failedTask *asynq.Task
	select {
	case failedTask = <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("ManualSync did not enqueue the source run")
	}

	if _, err := uuid.Parse(oldHandle); err != nil {
		t.Fatalf("source file handle is not a UUID: %v", err)
	}
	const faultName = "source_resource_review_write_fault"
	dropFault := "DROP TRIGGER IF EXISTS " + faultName + " ON source_file_versions; DROP FUNCTION IF EXISTS " + faultName + "();"
	t.Cleanup(func() { _ = f.db.Exec(dropFault).Error })
	// This schema-local trigger injects SQLSTATE 53100; it does not simulate a
	// physically full database volume.
	createFault := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.source_file_id = '%s' THEN
		RAISE EXCEPTION 'injected source disk_full write fault' USING ERRCODE = '53100';
	END IF;
	RETURN NEW;
END;
$$;
CREATE TRIGGER %s BEFORE INSERT ON source_file_versions FOR EACH ROW EXECUTE FUNCTION %s();`,
		faultName, oldHandle, faultName, faultName)
	require.NoError(t, f.db.Exec(createFault).Error)

	processErr := f.service.ProcessSync(f.ctx, failedTask)
	require.Error(t, processErr)
	require.Contains(t, processErr.Error(), "injected source disk_full write fault")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, processErr, &pgErr, "the fixture injects PostgreSQL SQLSTATE 53100")
	require.Equal(t, "53100", pgErr.Code)

	stillPublished := sourceResourceRegressionSearch(t, f, f.ds.ID, "diskFaultOldMarker")
	require.Len(t, stillPublished, 1, "a failed file-version write must not replace the published source")
	require.Equal(t, publishedCommit, stillPublished[0].Metadata["commit_sha"])
	require.Empty(t, sourceResourceRegressionSearch(t, f, f.ds.ID, "diskFaultNewMarker"),
		"the failed commit must not become searchable")
	oldView, err = f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err, "the prior published file handle must remain readable after the write fault")
	require.Equal(t, publishedCommit, oldView.CommitSHA)
	require.Contains(t, oldView.Content, "diskFaultOldMarker")
	otherView, err = f.knowledge.GetSourceFile(f.ctx, otherHandle)
	require.NoError(t, err, "the independent source handle must remain readable after the write fault")
	require.Equal(t, publishedCommit, otherView.CommitSHA)
	require.Contains(t, otherView.Content, "diskFaultOtherMarker")

	targets := types.SearchTargets{
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: f.kb.TenantID, SourceIDs: []string{other.ID}},
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, TenantID: f.kb.TenantID, KnowledgeIDs: []string{document.ID}},
	}
	readCtx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	for _, query := range []struct {
		text string
		want string
	}{
		{text: "diskFaultOtherMarker", want: other.ID},
		{text: "diskFaultOrdinaryDocumentMarker", want: document.ID},
	} {
		hits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
			QueryText: query.text, MatchCount: 10, DisableVectorMatch: true,
		})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		if query.want == document.ID {
			require.Equal(t, document.ID, hits[0].KnowledgeID)
		} else {
			require.Equal(t, other.ID, hits[0].Metadata["datasource_id"])
		}
	}
	excludedOldSourceHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "diskFaultOldMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Empty(t, excludedOldSourceHits, "the mixed read scope must not widen to source A while the write fault is active")

	// Remove the per-schema fault before consuming the retry queued by the existing
	// source-run failure contract.
	require.NoError(t, f.db.Exec(dropFault).Error)
	var retryTask *asynq.Task
	select {
	case retryTask = <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("failed source run did not enqueue its retry")
	}
	require.NoError(t, f.service.ProcessSync(f.ctx, retryTask),
		"the retried run must acquire admission and publish after the injected write fault is removed")

	retriedHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "diskFaultNewMarker")
	require.Len(t, retriedHits, 1)
	require.Equal(t, newCommit, retriedHits[0].Metadata["commit_sha"])
	newView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err, "the stable source-file handle must resolve the retried publication")
	require.Equal(t, newCommit, newView.CommitSHA)
	require.Contains(t, newView.Content, "diskFaultNewMarker")
	otherView, err = f.knowledge.GetSourceFile(f.ctx, otherHandle)
	require.NoError(t, err, "source B's original file handle must remain readable after source A retries")
	require.Equal(t, publishedCommit, otherView.CommitSHA)
	require.Contains(t, otherView.Content, "diskFaultOtherMarker")

	retriedScopeOtherHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "diskFaultOtherMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Len(t, retriedScopeOtherHits, 1, "the original mixed read lease must still read source B after retry")
	require.Equal(t, other.ID, retriedScopeOtherHits[0].Metadata["datasource_id"])
	retriedScopeDocumentHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "diskFaultOrdinaryDocumentMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Len(t, retriedScopeDocumentHits, 1, "the original mixed read lease must still read the ordinary document after retry")
	require.Equal(t, document.ID, retriedScopeDocumentHits[0].KnowledgeID)

	excludedSourceHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "diskFaultNewMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Empty(t, excludedSourceHits, "the mixed read scope must not widen to source A after retry")
}
