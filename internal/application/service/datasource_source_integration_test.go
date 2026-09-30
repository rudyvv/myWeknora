//go:build integration

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/gitlab"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSourceFirstJavaSnapshotIsPublishedAndSearchable(t *testing.T) {
	f := newJavaSourceFixture(t)
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, log)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	results, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: "getPushSchedule", MatchCount: 10, SkipContextEnrichment: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Contains(t, results[0].Content, "getPushSchedule")
	require.Equal(t, f.sha, results[0].Metadata["commit_sha"])
	var evidence struct {
		Source types.SourceEvidence `json:"source"`
	}
	require.NoError(t, json.Unmarshal(results[0].ChunkMetadata, &evidence))
	require.Equal(t, "src/Service.java", evidence.Source.Path)
	require.Equal(t, f.sha, evidence.Source.CommitSHA)
	require.Equal(t, types.SourceRange{StartByte: 0, EndByte: 140, StartLine: 1, EndLine: 8}, evidence.Source.Range)
	require.Contains(t, evidence.Source.GitLabURL, "/-/blob/"+f.sha+"/src/Service.java#L1-8")
	require.Contains(t, evidence.Source.Symbols, "Service.getPushSchedule")
	for _, params := range []types.SearchParams{
		{QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: true},
		{QueryText: "getPushSchedule", MatchCount: 10, DisableKeywordsMatch: true},
	} {
		hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, searchErr)
		require.NotEmpty(t, hits, "both real index routes must find the known Java method")
	}
}

func TestSourceMyBatisMapperXMLFactsRelationsScopesAndIndexes(t *testing.T) {
	files := map[string][]byte{
		"src/PushScheduleMapper.java": []byte("package demo; public interface PushScheduleMapper { Schedule getPushSchedule(Long id); }\n"),
		"src/mapper/PushScheduleMapper.xml": []byte(`<?xml version="1.0"?>
<!DOCTYPE mapper PUBLIC "-//mybatis.org//DTD Mapper 3.0//EN" "http://mybatis.org/dtd/mybatis-3-mapper.dtd">
<mapper namespace="demo.PushScheduleMapper">
  <resultMap id="Base" type="demo.Base"><id column="id" property="id"/></resultMap>
  <resultMap id="ScheduleMap" type="demo.Schedule" extends="Base"><association property="owner" resultMap="Base"/></resultMap>
  <sql id="columns"><include refid="baseColumns"/>ORDER BY id</sql>
  <sql id="baseColumns">id</sql>
  <select id="getPushSchedule" resultMap="ScheduleMap">
    SELECT id FROM push_schedule WHERE id = #{id} <include refid="columns"/>
  </select>
</mapper>`),
	}
	f := newJavaSourceFixture(t, files)
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)

	var javaFile, xmlFile types.SourceFile
	require.NoError(t, f.db.Where("data_source_id=? AND path=?", f.ds.ID, "src/PushScheduleMapper.java").Take(&javaFile).Error)
	require.NoError(t, f.db.Where("data_source_id=? AND path=?", f.ds.ID, "src/mapper/PushScheduleMapper.xml").Take(&xmlFile).Error)
	var snapshot types.SourceSnapshot
	require.NoError(t, f.db.Where("data_source_id=? AND state='published'", f.ds.ID).Take(&snapshot).Error)
	require.True(t, snapshot.RelationsStaged)
	require.Greater(t, snapshot.RelationCount, 0)

	var mapperRelation types.SourceCodeRelation
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='mapper_statement' AND determinacy='certain'", snapshot.ID).Take(&mapperRelation).Error)
	require.Equal(t, javaFile.ID, mapperRelation.FromFileID)
	require.Equal(t, xmlFile.ID, mapperRelation.ToFileID)
	require.Equal(t, "src/mapper/PushScheduleMapper.xml", mapperRelation.ToPath)
	require.Equal(t, snapshot.ID, mapperRelation.SnapshotID)
	var xmlVersion types.SourceFileVersion
	require.NoError(t, f.db.Where("source_file_id=? AND snapshot_id=?", xmlFile.ID, snapshot.ID).Take(&xmlVersion).Error)
	mapperTarget, err := f.knowledge.GetSourceFile(f.ctx, mapperRelation.ToFileID, mapperRelation.ToVersionID)
	require.NoError(t, err, "a certain cross-file edge must resolve through the restricted fixed-version source read")
	require.Equal(t, snapshot.ID, mapperTarget.SnapshotID)
	require.Equal(t, xmlVersion.ID, mapperTarget.FileVersionID)
	var mapperTargetRange types.SourceRange
	require.NoError(t, json.Unmarshal(mapperRelation.ToRange, &mapperTargetRange))
	require.LessOrEqual(t, mapperTargetRange.EndByte, len(mapperTarget.Content))
	require.Contains(t, mapperTarget.Content[mapperTargetRange.StartByte:mapperTargetRange.EndByte], `<select id="getPushSchedule"`)
	var storedFacts []types.ParsedSourceFact
	require.NoError(t, json.Unmarshal(xmlVersion.Facts, &storedFacts))
	factKinds := map[string]bool{}
	for _, fact := range storedFacts {
		factKinds[fact.Kind] = true
	}
	require.True(t, factKinds["mybatis_statement"])
	require.True(t, factKinds["mybatis_sql_fragment"])
	require.True(t, factKinds["mybatis_result_map_reference"])
	var includeRelation, resultMapRelation, associationRelation, extendsRelation, tableRelation types.SourceCodeRelation
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='include' AND from_key=? AND determinacy='certain'", snapshot.ID,
		"demo.PushScheduleMapper.columns -> include baseColumns").Take(&includeRelation).Error)
	require.Equal(t, xmlFile.ID, includeRelation.FromFileID)
	require.Equal(t, xmlFile.ID, includeRelation.ToFileID)
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='result_map' AND determinacy='certain'", snapshot.ID).Take(&resultMapRelation).Error)
	require.Equal(t, xmlFile.ID, resultMapRelation.FromFileID)
	require.Equal(t, xmlFile.ID, resultMapRelation.ToFileID)
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='result_map' AND from_key=? AND determinacy='certain'", snapshot.ID,
		"demo.PushScheduleMapper.ScheduleMap -> association Base").Take(&associationRelation).Error)
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='result_map' AND from_key=? AND determinacy='certain'", snapshot.ID,
		"demo.PushScheduleMapper.ScheduleMap -> extends Base").Take(&extendsRelation).Error)
	var includeRange, includeTargetRange types.SourceRange
	require.NoError(t, json.Unmarshal(includeRelation.FromRange, &includeRange))
	require.NoError(t, json.Unmarshal(includeRelation.ToRange, &includeTargetRange))
	require.Equal(t, `<include refid="baseColumns"/>`, string(files["src/mapper/PushScheduleMapper.xml"][includeRange.StartByte:includeRange.EndByte]))
	require.Equal(t, `<sql id="baseColumns">id</sql>`, string(files["src/mapper/PushScheduleMapper.xml"][includeTargetRange.StartByte:includeTargetRange.EndByte]))
	var extendsRange, extendsTargetRange types.SourceRange
	require.NoError(t, json.Unmarshal(extendsRelation.FromRange, &extendsRange))
	require.NoError(t, json.Unmarshal(extendsRelation.ToRange, &extendsTargetRange))
	require.Contains(t, string(files["src/mapper/PushScheduleMapper.xml"][extendsRange.StartByte:extendsRange.EndByte]), `extends="Base"`)
	require.Equal(t, `<resultMap id="Base" type="demo.Base"><id column="id" property="id"/></resultMap>`,
		string(files["src/mapper/PushScheduleMapper.xml"][extendsTargetRange.StartByte:extendsTargetRange.EndByte]))
	require.NoError(t, f.db.Where("snapshot_id=? AND kind='table_access' AND to_key='push_schedule' AND determinacy='certain'", snapshot.ID).Take(&tableRelation).Error)
	require.Empty(t, tableRelation.ToFileID, "database table facts must not fabricate a readable source-file target")
	require.Empty(t, tableRelation.ResolutionReason)

	for _, params := range []types.SearchParams{
		{QueryText: "getPushSchedule", MatchCount: 20, DisableVectorMatch: true},
		{QueryText: "getPushSchedule", MatchCount: 20, DisableKeywordsMatch: true},
	} {
		hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, searchErr)
		paths := map[string]bool{}
		for _, hit := range hits {
			var evidence struct {
				Source types.SourceEvidence `json:"source"`
			}
			require.NoError(t, json.Unmarshal(hit.ChunkMetadata, &evidence))
			paths[evidence.Source.Path] = true
		}
		require.True(t, paths["src/PushScheduleMapper.java"], "mapper method is searchable in each real index")
		require.True(t, paths["src/mapper/PushScheduleMapper.xml"], "XML statement is indexed independently of Java")
	}

	javaView, err := f.knowledge.GetSourceFile(f.ctx, javaFile.ID)
	require.NoError(t, err)
	require.Equal(t, string(files["src/PushScheduleMapper.java"]), javaView.Content)
	require.Empty(t, javaView.Relations, "single-file public scope must not expose the linked XML endpoint")

	pinned, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, KnowledgeIDs: []string{javaFile.ID, xmlFile.ID}},
	})
	require.NoError(t, err)
	defer release()
	linked, err := f.knowledge.GetSourceFile(pinned, javaFile.ID, javaView.FileVersionID)
	require.NoError(t, err)
	require.NotEmpty(t, linked.Relations)
	require.Contains(t, linked.Relations, mapperRelation)
	toolArgs, err := json.Marshal(map[string]any{"knowledge_id": javaFile.ID, "limit": 1})
	require.NoError(t, err)
	toolResult, err := agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, KnowledgeIDs: []string{javaFile.ID, xmlFile.ID}},
	}).Execute(pinned, toolArgs)
	require.NoError(t, err)
	require.True(t, toolResult.Success, toolResult.Error)
	analysis, ok := toolResult.Data["source_analysis"].(map[string]interface{})
	require.True(t, ok, "Agent chunk reads inside the existing question scope expose source facts and relations")
	require.Equal(t, javaView.FileVersionID, analysis["file_version_id"])
	require.NotEmpty(t, analysis["facts"])
	var targetEvidence map[string]interface{}
	for _, raw := range analysis["relations"].([]map[string]interface{}) {
		if raw["kind"] == "mapper_statement" {
			targetEvidence, _ = raw["target_evidence"].(map[string]interface{})
		}
	}
	require.NotNil(t, targetEvidence)
	require.Equal(t, xmlFile.ID, targetEvidence["knowledge_id"])
	require.Equal(t, xmlVersion.ID, targetEvidence["file_version_id"])
	require.Equal(t, xmlVersion.SHA256, targetEvidence["sha256"])
	require.Equal(t, xmlFile.Path, targetEvidence["path"])
	require.Contains(t, targetEvidence["snippet"], "getPushSchedule")
	require.Contains(t, toolResult.Output, "<source_analysis>")

	probeRelations := make([]types.SourceCodeRelation, 110)
	probeIDs := make(map[string]bool, len(probeRelations))
	for i := range probeRelations {
		probeRelations[i] = types.SourceCodeRelation{
			ID: uuid.NewString(), TenantID: 1, DataSourceID: f.ds.ID, SnapshotID: snapshot.ID,
			Kind: "pagination_probe", FromFileID: javaFile.ID, FromVersionID: javaView.FileVersionID,
			FromPath: javaFile.Path, FromKey: fmt.Sprintf("probe-%03d", i),
			FromRange: types.JSON(fmt.Sprintf(`{"start_byte":%d,"end_byte":%d,"start_line":1,"end_line":1}`, i, i+1)),
			ToFileID:  xmlFile.ID, ToVersionID: xmlVersion.ID, ToPath: xmlFile.Path,
			ToKey: fmt.Sprintf("probe-%03d", i), ToRange: mapperRelation.ToRange,
			Determinacy: "certain", Quality: "structural", Context: types.JSON(`[]`),
		}
		probeIDs[probeRelations[i].ID] = true
	}
	require.NoError(t, f.db.Create(&probeRelations).Error)
	agentFirstArgs, _ := json.Marshal(map[string]any{"knowledge_id": javaFile.ID, "limit": 1})
	agentFirst, err := agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, KnowledgeIDs: []string{javaFile.ID, xmlFile.ID}},
	}).Execute(pinned, agentFirstArgs)
	require.NoError(t, err)
	firstAnalysis := agentFirst.Data["source_analysis"].(map[string]interface{})
	firstAgentRelations := firstAnalysis["relations"].([]map[string]interface{})
	require.Len(t, firstAgentRelations, 20, "Agent evidence pages stay within a small output budget")
	require.True(t, firstAnalysis["relations_truncated"].(bool))
	agentCursor := firstAnalysis["relations_next_cursor"].(string)
	agentNextArgs, _ := json.Marshal(map[string]any{"knowledge_id": javaFile.ID, "limit": 1, "relation_cursor": agentCursor})
	agentNext, err := agenttools.NewListKnowledgeChunksTool(f.knowledge, f.chunks, types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, KnowledgeIDs: []string{javaFile.ID, xmlFile.ID}},
	}).Execute(pinned, agentNextArgs)
	require.NoError(t, err)
	nextAnalysis := agentNext.Data["source_analysis"].(map[string]interface{})
	nextAgentRelations := nextAnalysis["relations"].([]map[string]interface{})
	require.Len(t, nextAgentRelations, 20)
	var continuedTarget map[string]interface{}
	for _, relation := range nextAgentRelations {
		if target, ok := relation["target_evidence"].(map[string]interface{}); ok {
			continuedTarget = target
			break
		}
	}
	require.NotNil(t, continuedTarget, "continuation target reads must not reuse the main-file cursor")
	require.Equal(t, xmlFile.ID, continuedTarget["knowledge_id"])
	require.Equal(t, xmlVersion.ID, continuedTarget["file_version_id"])
	require.Contains(t, continuedTarget["snippet"], "getPushSchedule")
	firstAgentIDs := map[string]bool{}
	for _, relation := range firstAgentRelations {
		firstAgentIDs[relation["id"].(string)] = true
	}
	for _, relation := range nextAgentRelations {
		require.False(t, firstAgentIDs[relation["id"].(string)], "Agent relation cursor advances without repeating edges")
	}
	firstPage, err := f.knowledge.GetSourceFile(pinned, javaFile.ID, javaView.FileVersionID)
	require.NoError(t, err)
	require.Len(t, firstPage.Relations, 100)
	require.True(t, firstPage.RelationsTruncated)
	require.NotEmpty(t, firstPage.RelationsNextCursor)
	secondCtx := source.WithRelationCursor(pinned, firstPage.RelationsNextCursor)
	secondPage, err := f.knowledge.GetSourceFile(secondCtx, javaFile.ID, javaView.FileVersionID)
	require.NoError(t, err)
	require.False(t, secondPage.RelationsTruncated)
	require.Empty(t, secondPage.RelationsNextCursor)
	seenProbeIDs := make(map[string]bool, len(probeIDs))
	for _, relation := range append(firstPage.Relations, secondPage.Relations...) {
		if probeIDs[relation.ID] {
			require.False(t, seenProbeIDs[relation.ID], "keyset pagination must not repeat relations")
			seenProbeIDs[relation.ID] = true
		}
	}
	require.Len(t, seenProbeIDs, len(probeIDs), "continuation must return every relation after the first 100")
	_, err = f.knowledge.GetSourceFile(source.WithRelationCursor(pinned, firstPage.RelationsNextCursor), xmlFile.ID, xmlVersion.ID)
	require.ErrorContains(t, err, "invalid source relation cursor", "cursor is bound to one exact snapshot/file/version")
	xmlView, err := f.knowledge.GetSourceFile(pinned, xmlFile.ID)
	require.NoError(t, err)
	require.Equal(t, string(files["src/mapper/PushScheduleMapper.xml"]), xmlView.Content)

	// A failed relation stage is part of the snapshot build, not a partial
	// update: the preceding publication and its pinned relation remain usable.
	actualSnapshots := f.service.sourceSnapshots
	f.service.sourceSnapshots = relationStageFailure{SourceSnapshotRepository: actualSnapshots}
	changedXML := strings.Replace(string(files["src/mapper/PushScheduleMapper.xml"]), "push_schedule", "push_schedule_next", 1)
	f.advanceFiles(map[string][]byte{"src/mapper/PushScheduleMapper.xml": []byte(changedXML)})
	failedLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	failedPayload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: failedLog.ID, Trigger: "manual"})
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, failedPayload)), "injected relation staging failure")
	f.service.sourceSnapshots = actualSnapshots
	failedResult, err := f.service.GetSyncLog(f.ctx, failedLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failedResult.Status)
	var stillPublished types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id=?", f.ds.ID).Take(&stillPublished).Error)
	require.Equal(t, snapshot.ID, stillPublished.SnapshotID)
	failedRun, err := actualSnapshots.GetRun(f.ctx, 1, f.ds.ID, failedLog.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedRun.Snapshot.State)
	var failedRelationCount int64
	require.NoError(t, f.db.Model(&types.SourceCodeRelation{}).Where("snapshot_id=?", failedRun.Snapshot.ID).Count(&failedRelationCount).Error)
	require.Zero(t, failedRelationCount)
	stillPinned, err := f.knowledge.GetSourceFile(pinned, javaFile.ID, javaView.FileVersionID)
	require.NoError(t, err)
	require.Contains(t, stillPinned.Relations, mapperRelation)

	// An empty complete manifest clears the active relation set without
	// rewriting the prior snapshot pinned by this read lease.
	f.advanceFiles(map[string][]byte{
		"src/PushScheduleMapper.java":       nil,
		"src/mapper/PushScheduleMapper.xml": nil,
	})
	nextLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	nextPayload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: nextLog.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, nextPayload)))
	var emptySnapshot types.SourceSnapshot
	require.NoError(t, f.db.Joins("JOIN source_publications p ON p.snapshot_id=source_snapshots.id").Where("p.data_source_id=?", f.ds.ID).Take(&emptySnapshot).Error)
	require.NotEqual(t, snapshot.ID, emptySnapshot.ID)
	require.True(t, emptySnapshot.RelationsStaged)
	require.Zero(t, emptySnapshot.RelationCount)
	var retained int64
	require.NoError(t, f.db.Model(&types.SourceCodeRelation{}).Where("snapshot_id=? AND id=?", snapshot.ID, mapperRelation.ID).Count(&retained).Error)
	require.EqualValues(t, 1, retained)
	oldPinned, err := f.knowledge.GetSourceFile(pinned, javaFile.ID, javaView.FileVersionID)
	require.NoError(t, err)
	require.Equal(t, snapshot.ID, oldPinned.SnapshotID)
	require.Contains(t, oldPinned.Relations, mapperRelation)
	_, err = f.knowledge.GetSourceFile(f.ctx, javaFile.ID)
	require.Error(t, err, "the empty current publication must not expose a removed source file")
	_, err = f.knowledge.GetSourceFile(f.ctx, mapperRelation.ToFileID, mapperRelation.ToVersionID)
	require.Error(t, err, "the former cross-file relation target must not bypass the published read scope after deletion")
}

func TestMalformedMyBatisXMLFallsBackAndPublishesReadableSnapshot(t *testing.T) {
	const path = "src/mapper/BrokenMapper.xml"
	raw := []byte(`<mapper namespace="demo.M"><select id="x">SELECT * FROM t</mapper>`)
	f := newJavaSourceFixture(t, map[string][]byte{path: raw})
	unsafeXML := []byte(`<!DOCTYPE mapper SYSTEM "https://attacker.invalid/evil.dtd"><mapper namespace="demo.Unsafe"/>`)
	_, err := source.ParseFile(f.ctx, os.Getenv("SOURCE_PARSER_URL"), "src/mapper/Unsafe.xml", unsafeXML)
	require.Error(t, err, "explicitly forbidden external DTDs must remain rejected, not downgraded")
	f.parseCount.Store(0)
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)),
		"readable malformed XML must fall back instead of aborting snapshot publication")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	require.EqualValues(t, 2, f.parseCount.Load(), "both the malformed mapper and ordinary Java file reach the parser HTTP endpoint")

	var file types.SourceFile
	require.NoError(t, f.db.Where("data_source_id=? AND path=?", f.ds.ID, path).Take(&file).Error)
	view, err := f.knowledge.GetSourceFile(f.ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, string(raw), view.Content, "the published source read preserves the exact original bytes")
	require.Equal(t, "text_fallback", view.Quality)
	var facts []types.ParsedSourceFact
	require.NoError(t, json.Unmarshal(view.Facts, &facts))
	require.Empty(t, facts, "unreliable XML structure must not create structural facts")
	var diagnostics []types.ParsedSourceDiagnostic
	require.NoError(t, json.Unmarshal(view.Diagnostics, &diagnostics))
	var syntaxDiagnostic *types.ParsedSourceDiagnostic
	for index := range diagnostics {
		if diagnostics[index].Code == "mybatis_xml_syntax_fallback" {
			syntaxDiagnostic = &diagnostics[index]
			break
		}
	}
	require.NotNil(t, syntaxDiagnostic, "the fallback quality must include an actionable parse diagnostic")
	require.Equal(t, "</mapper>", string(raw[syntaxDiagnostic.Range.StartByte:syntaxDiagnostic.Range.EndByte]),
		"diagnostic coordinates must point to the original mismatched closing tag")

	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: "SELECT", MatchCount: 20, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	foundIndexedFallback := false
	for _, hit := range hits {
		var evidence struct {
			Source types.SourceEvidence `json:"source"`
		}
		if json.Unmarshal(hit.ChunkMetadata, &evidence) == nil && evidence.Source.Path == path && strings.Contains(hit.Content, "SELECT * FROM t") {
			foundIndexedFallback = true
			break
		}
	}
	require.True(t, foundIndexedFallback, "fallback text must be persisted in the published keyword index")
}

type relationStageFailure struct {
	interfaces.SourceSnapshotRepository
}

func (r relationStageFailure) StageRelations(context.Context, uint64, string, string, []types.SourceCodeRelation) error {
	return errors.New("injected relation staging failure")
}

func TestSourcePublishedChunksRemainReadOnlyButDescriptionMayChange(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	stored, err := f.chunks.GetChunkByID(f.ctx, hits[0].ID)
	require.NoError(t, err)
	change := *stored
	change.Content = "manually rewritten source"
	require.ErrorContains(t, f.chunks.UpdateChunk(f.ctx, &change), "Git-managed")
	require.ErrorContains(t, f.chunks.DeleteChunk(f.ctx, stored.ID), "Git-managed")
	change = *stored
	change.IsEnabled = false
	require.ErrorContains(t, f.chunks.UpdateChunk(f.ctx, &change), "Git-managed")
	_, err = f.knowledge.ReparseKnowledge(f.ctx, stored.KnowledgeID, nil)
	require.ErrorContains(t, err, "Git-managed")
	require.ErrorContains(t, f.knowledge.DeleteKnowledge(f.ctx, stored.KnowledgeID), "Git-managed")
	_, err = f.knowledge.MoveKnowledgeToFolder(f.ctx, f.kb.ID, []string{stored.KnowledgeID}, "manual")
	require.ErrorContains(t, err, "Git-managed")
	require.ErrorContains(t, f.knowledge.RequestKnowledgeSummaryRefresh(f.ctx, stored.KnowledgeID), "Git-managed")
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"regenerate questions", func() error { _, err := f.knowledge.RegenerateChunkQuestions(f.ctx, stored.ID); return err }},
		{"generate first summary", func() error { _, err := f.knowledge.RegenerateKnowledgeSummary(f.ctx, stored.KnowledgeID); return err }},
		{"delete all file chunks", func() error { return f.chunks.DeleteChunksByKnowledgeID(f.ctx, stored.KnowledgeID) }},
		{"delete chunks by file list", func() error { return f.chunks.DeleteByKnowledgeList(f.ctx, []string{stored.KnowledgeID}) }},
	} {
		t.Run(operation.name, func(t *testing.T) { require.ErrorContains(t, operation.run(), "Git-managed") })
	}
	require.NoError(t, f.knowledge.UpdateKnowledge(f.ctx, &types.Knowledge{ID: stored.KnowledgeID, Description: "排班服务说明", DescriptionSpecified: true}))
	unchanged, err := f.chunks.GetChunkByID(f.ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, stored.Content, unchanged.Content)
	require.True(t, unchanged.IsEnabled)
	info, err := f.knowledge.GetKnowledgeByID(f.ctx, stored.KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, "排班服务说明", info.Description)
}

func TestSourcePublishedFileDownloadPreservesOriginalBytes(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	reader, filename, err := f.knowledge.GetKnowledgeFile(f.ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	defer reader.Close()
	original, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "Service.java", filename)
	require.Equal(t, []byte("package demo;\r\n// 中文\r\npublic class Service {\r\n @Deprecated\r\n public String getPushSchedule(String 名称) {\r\n  return \"预约\";\r\n }\r\n}\r\n"), original)
	view, err := f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, string(original), view.Content)
	require.Equal(t, f.sha, view.CommitSHA)
	require.Equal(t, "src/Service.java", view.Path)
	require.NotEmpty(t, view.FileVersionID)
	require.NotEmpty(t, view.Symbols)
	// Optional UI demonstration artifacts come only from public read results.
	if directory := os.Getenv("SOURCE_TEST_DEMO_DIR"); directory != "" {
		finished, readErr := f.service.GetSyncLog(f.ctx, log.ID)
		require.NoError(t, readErr)
		var progress types.SyncResult
		require.NoError(t, json.Unmarshal(finished.Result, &progress))
		require.NoError(t, os.MkdirAll(directory, 0755))
		fileJSON, encodeErr := json.Marshal(map[string]any{"data": view})
		require.NoError(t, encodeErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "source-file.json"), fileJSON, 0600))
		runJSON, encodeErr := json.Marshal(progress.Source)
		require.NoError(t, encodeErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "source-run.json"), runJSON, 0600))
	}
	pinned, err := f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID, view.FileVersionID)
	require.NoError(t, err)
	require.Equal(t, view.CommitSHA, pinned.CommitSHA)
	_, err = f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID, uuid.NewString())
	require.Error(t, err)
	foreign := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2))
	_, err = f.knowledge.GetSourceFile(foreign, hits[0].KnowledgeID)
	require.Error(t, err)
	// Revoking a source hides its identity as well as its body on public reads.
	require.NoError(t, f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error)
	_, err = f.knowledge.GetKnowledgeByID(f.ctx, hits[0].KnowledgeID)
	require.Error(t, err)
}

func TestSourceSearchHonorsFileTenantAndTagScopes(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	fileID := hits[0].KnowledgeID
	for _, keywordsOnly := range []bool{true, false} {
		params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, KnowledgeIDs: []string{fileID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly}
		hits, err = f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.NotEmpty(t, hits)
		params.KnowledgeIDs = []string{uuid.NewString()}
		hits, err = f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.Empty(t, hits)
	}
	foreign := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2))
	_, err = f.kbs.HybridSearch(foreign, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
	_, err = f.kbs.HybridSearch(f.ctx, uuid.NewString(), types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "排班"}
	require.NoError(t, f.db.Create(tag).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, fileID, []string{tag.ID}))
	for _, keywordsOnly := range []bool{true, false} {
		for _, tagID := range []string{tag.ID, uuid.NewString()} {
			params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, TagIDs: []string{tagID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly}
			hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
			require.NoError(t, err)
			if tagID == tag.ID {
				require.NotEmpty(t, hits, "both index routes must honor document tags")
			} else {
				require.Empty(t, hits)
			}
		}
	}
	for _, tagID := range []string{tag.ID, uuid.NewString()} {
		grep := agenttools.NewSourceAwareGrepChunksTool(f.db, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, TagIDs: []string{tagID}}})
		result, err := grep.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
		require.NoError(t, err)
		if tagID == tag.ID {
			require.Greater(t, result.Data["result_count"].(int), 0)
		} else {
			require.Equal(t, 0, result.Data["result_count"])
		}
	}
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, fileID, nil))
	for _, keywordsOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, TagIDs: []string{tag.ID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly})
		require.NoError(t, err)
		require.Empty(t, hits, "removing a file tag must affect both indexes without rebuilding the snapshot")
	}
}

func TestSourceSearchSeparatesSamePathAcrossRepositorySources(t *testing.T) {
	f := newJavaSourceFixture(t)
	second := &types.DataSource{ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID, Name: "independent second source", Type: f.ds.Type, Status: types.DataSourceStatusPaused, Config: append(types.JSON{}, f.ds.Config...)}
	_, err := f.service.CreateDataSource(f.ctx, second)
	require.NoError(t, err)
	for _, ds := range []*types.DataSource{f.ds, second} {
		log, err := f.service.ManualSync(f.ctx, ds.ID)
		require.NoError(t, err)
		payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
		require.NoError(t, err)
		require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	}
	all, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.NotEqual(t, all[0].KnowledgeID, all[1].KnowledgeID, "same path in two sources must retain separate file identities")
	for _, keywordsOnly := range []bool{true, false} {
		for _, id := range []string{f.ds.ID, second.ID, uuid.NewString()} {
			hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1, SourceIDs: []string{id}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly})
			require.NoError(t, err)
			if id != f.ds.ID && id != second.ID {
				require.Empty(t, hits)
				continue
			}
			require.Len(t, hits, 1)
			require.Equal(t, id, hits[0].Metadata["datasource_id"], "repository scope must apply before topK")
		}
	}
}

func TestSourceInvalidEmbeddingNeverPublishes(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedVector = []float32{0, 0, 0}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)), "zero")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestSourceUnreadableSelectedJavaPreventsPublication(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Broken.java": {0xff, 0xfe, 'c', 0, 'l', 0}})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)), "not readable")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(finished.Result, &progress))
	require.True(t, progress.Source.Snapshot.ManifestComplete)
	require.Len(t, progress.Source.Members, 3)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
}

type sourceNoObjectStorage struct{ interfaces.FileService }

func (sourceNoObjectStorage) GetFile(context.Context, string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func TestSourceKeywordIndexFailureNeverPublishesFirstSnapshot(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	}()
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("source embedding boundary not reached")
	}
	// Controlled failure of the actual external keyword index, after preflight.
	require.NoError(t, f.db.Exec("DROP INDEX embeddings_search_idx").Error)
	f.embedRelease <- struct{}{}
	require.ErrorContains(t, <-done, "keyword index")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(finished.Result, &progress))
	require.Equal(t, "failed", progress.Source.Snapshot.State)
	for _, member := range progress.Source.Members {
		if member.Status != "parsed" {
			continue
		}
		chunks, listErr := f.chunks.ListChunksByKnowledgeID(f.ctx, member.SourceFileID)
		require.NoError(t, listErr)
		require.Empty(t, chunks)
	}
	// A damaged enabled flag cannot turn a failed manifest into publication.
	require.NoError(t, f.db.Exec("UPDATE chunks SET is_enabled=true; UPDATE embeddings SET is_enabled=true").Error)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, DisableKeywordsMatch: true, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
	for _, member := range progress.Source.Members {
		if member.Status != "parsed" {
			continue
		}
		chunks, listErr := f.chunks.ListChunksByKnowledgeID(f.ctx, member.SourceFileID)
		require.NoError(t, listErr)
		require.Empty(t, chunks)
	}
	// Ordinary knowledge rows and enabled flags cannot bypass typed publication.
	for _, member := range progress.Source.Members {
		if member.Status == "parsed" {
			require.NoError(t, f.db.Create(&types.Knowledge{ID: member.SourceFileID, TenantID: 1, KnowledgeBaseID: f.kb.ID, Type: types.KnowledgeTypeSource, Title: member.Path, CustomMetadata: types.JSON(`{}`)}).Error)
		}
	}
	grep := agenttools.NewSourceAwareGrepChunksTool(f.db, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}})
	grepped, err := grep.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.True(t, grepped.Success)
	require.Equal(t, 0, grepped.Data["result_count"])
}

func TestSourceStagingChunksCannotBeReadWhileEmbeddingIsPending(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	}()
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("source embedding did not reach the controlled boundary")
	}
	running, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(running.Result, &progress))
	require.Equal(t, "indexing", progress.Source.Snapshot.State)
	var fileID string
	for _, member := range progress.Source.Members {
		if member.Status == "parsed" {
			fileID = member.SourceFileID
		}
	}
	require.NotEmpty(t, fileID)
	chunks, err := f.chunks.ListChunksByKnowledgeID(f.ctx, fileID)
	require.NoError(t, err)
	require.Empty(t, chunks, "staging must be absent from the public chunk list, even for its stable file ID")
	results, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, results)
	// Release through a separate send-compatible channel so cleanup remains safe
	// when an assertion fails before publication.
	f.embedRelease <- struct{}{}
	require.NoError(t, <-done)
	chunks, err = f.chunks.ListChunksByKnowledgeID(f.ctx, fileID)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
}

type javaSourceFixture struct {
	ctx              context.Context
	db               *gorm.DB
	service          *DataSourceService
	kbs              interfacesKnowledgeBaseService
	ds               *types.DataSource
	kb               *types.KnowledgeBase
	sha              string
	chunks           interfaces.ChunkService
	knowledge        interfaces.KnowledgeService
	embedStarted     chan struct{}
	embedRelease     chan struct{}
	embedVector      []float32
	embeddingForText func(string) []float32
	embedCount       atomic.Int64
	parseCount       atomic.Int64
	parseStarted     chan struct{}
	parseRelease     chan struct{}
	advanceFiles     func(map[string][]byte) string
	modelService     interfaces.ModelService
	advanceJava      func(string) string
	shares           interfaces.KBShareService
	agentShares      interfaces.AgentShareService
}

// A local alias keeps the fixture's public boundary explicit.
type interfacesKnowledgeBaseService interface {
	HybridSearch(context.Context, string, types.SearchParams) ([]*types.SearchResult, error)
}

func newJavaSourceFixture(t *testing.T, extraFiles ...map[string][]byte) *javaSourceFixture {
	t.Helper()
	f := &javaSourceFixture{}
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	python := os.Getenv("SOURCE_TEST_PYTHON")
	cache := os.Getenv("SOURCE_PARSER_CACHE")
	if dsn == "" || python == "" || cache == "" {
		t.Fatal("integration requires SOURCE_TEST_POSTGRES_DSN, SOURCE_TEST_PYTHON and prefetched SOURCE_PARSER_CACHE")
	}
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	// These fixtures can create/drop schemas only in the dedicated test database.
	require.Equal(t, "/source_test", address.Path)
	require.Equal(t, "127.0.0.1", address.Hostname())
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	schema := "source_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_search").Error)
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		sqlDB, _ := admin.DB()
		_ = sqlDB.Close()
	})
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}, &types.Model{}, &types.DataSource{}, &types.SyncLog{}, &types.KnowledgeTag{}, &types.KnowledgeTagRelation{}, &types.Organization{}, &types.OrganizationTenantMember{}, &types.KnowledgeBaseShare{}))
	require.NoError(t, db.Exec(`CREATE TABLE embeddings (
		id BIGSERIAL PRIMARY KEY, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ,
		source_id TEXT NOT NULL, source_type INTEGER NOT NULL, chunk_id TEXT, knowledge_id TEXT,
		knowledge_base_id TEXT, tag_id TEXT, content TEXT NOT NULL, dimension INTEGER NOT NULL,
		embedding HALFVEC NOT NULL, is_enabled BOOLEAN DEFAULT TRUE);
		CREATE INDEX embeddings_search_idx ON embeddings USING bm25 (id, content, knowledge_base_id, knowledge_id, tag_id) WITH (key_field='id');
		CREATE INDEX embeddings_test_vector_idx ON embeddings USING hnsw ((embedding::halfvec(3)) halfvec_cosine_ops) WHERE dimension=3;`).Error)

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	sourceMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000102_source_snapshots.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(sourceMigration)).Error)
	leaseMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000103_source_read_leases.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(leaseMigration)).Error)
	incrementalMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000104_source_incremental_artifacts.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(incrementalMigration)).Error)
	relationMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000107_source_code_relations.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(relationMigration)).Error)
	parser := exec.Command(python, filepath.Join(root, "sourceparser", "server.py"), "--host", "127.0.0.1", "--port", "0")
	parser.Env = append(os.Environ(), "SOURCE_PARSER_CACHE="+cache)
	stdout, err := parser.StdoutPipe()
	require.NoError(t, err)
	parser.Stderr = os.Stderr
	require.NoError(t, parser.Start())
	t.Cleanup(func() { _ = parser.Process.Kill(); _ = parser.Wait() })
	startup := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			startup <- scanner.Text()
		} else {
			startup <- ""
		}
	}()
	var ready struct {
		Port int `json:"port"`
	}
	select {
	case line := <-startup:
		require.NoError(t, json.Unmarshal([]byte(line), &ready))
	case <-time.After(10 * time.Second):
		t.Fatal("real Java parser did not start")
	}
	parserAddress, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", ready.Port))
	require.NoError(t, err)
	forward := httputil.NewSingleHostReverseProxy(parserAddress)
	parserProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/parse" {
			f.parseCount.Add(1)
			if f.parseStarted != nil {
				select {
				case f.parseStarted <- struct{}{}:
				default:
				}
				select {
				case <-f.parseRelease:
				case <-r.Context().Done():
					return
				}
			}
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(parserProxy.Close)
	t.Setenv("SOURCE_PARSER_URL", parserProxy.URL)
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.embedCount.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if f.embedStarted != nil {
			select {
			case f.embedStarted <- struct{}{}:
			default:
			}
			select {
			case <-f.embedRelease:
			case <-r.Context().Done():
				return
			}
		}
		items := make([]map[string]any, len(request.Input))
		vector := f.embedVector
		if vector == nil {
			vector = []float32{1, 0, 0}
		}
		for i := range items {
			if f.embeddingForText != nil {
				vector = f.embeddingForText(request.Input[i])
			}
			items[i] = map[string]any{"index": i, "embedding": vector}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(modelServer.Close)
	tenant := &types.Tenant{ID: 1, Name: "source integration", Business: "test", RetrieverEngines: types.RetrieverEngines{Engines: types.GetRetrieverEngineMapping()["postgres"]}}
	require.NoError(t, db.Create(tenant).Error)
	model := &types.Model{ID: uuid.NewString(), TenantID: 1, Name: "source-test-model", Type: types.ModelTypeEmbedding, Source: types.ModelSourceRemote, Status: types.ModelStatusActive,
		Parameters: types.ModelParameters{BaseURL: modelServer.URL, Provider: "openai", InterfaceType: "openai", EmbeddingParameters: types.EmbeddingParameters{Dimension: 3}}}
	require.NoError(t, db.Create(model).Error)
	kb := &types.KnowledgeBase{ID: uuid.NewString(), TenantID: 1, Name: "Java source", Type: "document", EmbeddingModelID: model.ID,
		IndexingStrategy: types.IndexingStrategy{KeywordEnabled: true, VectorEnabled: true}}
	require.NoError(t, db.Create(kb).Error)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	ctx, err = access.WithKBTaskWrite(ctx, kb, 1)
	require.NoError(t, err)

	repoDir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, e := cmd.CombinedOutput()
		require.NoError(t, e, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "--initial-branch=main")
	git("config", "core.autocrlf", "false")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "user.name", "Source integration")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "Service.java"), []byte("package demo;\r\n// 中文\r\npublic class Service {\r\n @Deprecated\r\n public String getPushSchedule(String 名称) {\r\n  return \"预约\";\r\n }\r\n}\r\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("outside chosen Java scope\n"), 0644))
	for _, files := range extraFiles {
		for name, content := range files {
			target := filepath.Join(repoDir, filepath.FromSlash(name))
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, content, 0644))
		}
	}
	git("add", ".")
	git("commit", "-m", "Java source fixture")
	sha := git("rev-parse", "HEAD")
	f.advanceFiles = func(changes map[string][]byte) string {
		for name, content := range changes {
			target := filepath.Join(repoDir, filepath.FromSlash(name))
			if content == nil {
				require.NoError(t, os.Remove(target))
				continue
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, content, 0644))
		}
		git("add", ".")
		git("commit", "-m", "Complete manifest change")
		sha = git("rev-parse", "HEAD")
		return sha
	}
	f.advanceJava = func(content string) string {
		return f.advanceFiles(map[string][]byte{"src/Service.java": []byte(content)})
	}
	var gitlabServer *httptest.Server
	gitlabServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			fmt.Fprint(w, `{"id":1}`)
		case "/api/v4/personal_access_tokens/self":
			fmt.Fprint(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
		case "/api/v4/projects/123":
			fmt.Fprintf(w, `{"id":123,"http_url_to_repo":%q}`, gitlabServer.URL+"/repo.git")
		case "/api/v4/projects/123/repository/branches/main":
			fmt.Fprintf(w, `{"name":"main","commit":{"id":%q}}`, sha)
		case "/repo.git/info/refs", "/repo.git/git-upload-pack":
			args := []string{"upload-pack", "--stateless-rpc"}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				fmt.Fprint(w, "001e# service=git-upload-pack\n0000")
				args = append(args, "--advertise-refs")
			} else {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			}
			cmd := exec.Command("git", append(args, repoDir)...)
			cmd.Stdin, cmd.Stdout = r.Body, w
			require.NoError(t, cmd.Run())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabServer.Close)
	config, err := json.Marshal(map[string]any{"type": "gitlab", "credentials": map[string]any{"base_url": gitlabServer.URL, "access_token": "fixture-token"},
		"settings": map[string]any{"content_mode": "source", "projects": []any{map[string]any{"project_id": "123", "ref": "main", "paths": []string{"src"}}}}})
	require.NoError(t, err)
	ds := &types.DataSource{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: kb.ID, Name: "Java fixture", Type: "gitlab", Status: types.DataSourceStatusPaused, Config: types.JSON(config)}
	dsRepo := repository.NewDataSourceRepository(db)
	require.NoError(t, dsRepo.Create(ctx, ds))
	engines := retriever.NewRetrieveEngineRegistry(nil, nil)
	require.NoError(t, engines.Register(retriever.NewKVHybridRetrieveEngine(pgrepo.NewSourceAwarePostgresRetrieveEngineRepository(db), types.PostgresRetrieverEngineType)))
	kbRepo := repository.NewKnowledgeBaseRepository(db)
	models := repository.NewModelRepository(db)
	modelService := NewModelService(models, kbRepo, nil, nil, nil, nil)
	f.shares = NewKBShareService(repository.NewKBShareRepository(db), repository.NewOrganizationRepository(db), kbRepo, repository.NewSourceAwareKnowledgeRepository(db), repository.NewSourceAwareChunkRepository(db), nil)
	f.agentShares = NewAgentShareService(repository.NewAgentShareRepository(db), repository.NewTenantDisabledSharedAgentRepository(db), repository.NewOrganizationRepository(db), repository.NewCustomAgentRepository(db), repository.NewUserRepository(db), nil)
	kbs := NewKnowledgeBaseService(kbRepo, repository.NewSourceAwareKnowledgeRepository(db), repository.NewSourceAwareChunkRepository(db), nil, f.shares, modelService, engines, nil, repository.NewTenantRepository(db), nil, nil, nil, nil, nil, nil, dsRepo, repository.NewSyncLogRepository(db), nil, nil, nil, nil, f.agentShares)
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(gitlab.NewConnector()))
	svc := NewDataSourceService(dsRepo, repository.NewSyncLogRepository(db), nil, kbs, kbDeleteTaskEnqueuer{}, registry, datasource.NewScheduler(dsRepo, repository.NewSyncLogRepository(db), kbDeleteTaskEnqueuer{}), repository.NewTenantRepository(db), nil, nil, engines, nil, models, repository.NewSourceSnapshotRepository(db), modelService).(*DataSourceService)
	f.ctx, f.db, f.service, f.kbs, f.ds, f.kb, f.sha = ctx, db, svc, kbs, ds, kb, sha
	f.modelService = modelService
	f.chunks = NewChunkService(repository.NewSourceAwareChunkRepository(db), repository.NewSourceAwareKnowledgeRepository(db), kbRepo, modelService, engines, nil, nil, nil, kbs)
	f.knowledge = &knowledgeService{repo: repository.NewSourceAwareKnowledgeRepository(db), kbService: kbs, kbShareService: f.shares, chunkRepo: repository.NewSourceAwareChunkRepository(db), chunkService: f.chunks, modelService: modelService, retrieveEngine: engines, task: kbDeleteTaskEnqueuer{}, fileSvc: sourceNoObjectStorage{}, tagRepo: repository.NewKnowledgeTagRepository(db)}
	return f
}
