//go:build integration

package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSourceSharedAgentRoutesAndQuestionRemainBoundToOriginalAgent(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t)
	f.Sync()
	require.NoError(t, f.DB.AutoMigrate(&types.CustomAgent{}, &types.AgentShare{}, &types.TenantDisabledSharedAgent{}))
	require.NoError(t, f.DB.Create(&types.User{ID: "owner", Username: "owner", Email: "owner@example.invalid", TenantID: 1}).Error)
	org := &types.Organization{ID: uuid.NewString(), Name: "source agent sharing", OwnerID: "owner", OwnerTenantID: 1, InviteCode: "source-agent-fixture"}
	require.NoError(t, f.DB.Create(org).Error)
	for _, member := range []*types.OrganizationTenantMember{{ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 1, Role: types.OrgRoleAdmin, RepresentativeUserID: "owner"}, {ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 2, Role: types.OrgRoleViewer, RepresentativeUserID: "owner"}} {
		require.NoError(t, f.DB.Create(member).Error)
	}
	agents := []*types.CustomAgent{{ID: uuid.NewString(), Name: "original", TenantID: 1, Config: types.CustomAgentConfig{ModelID: "fixture-model", RerankModelID: "fixture-rerank", KBSelectionMode: "selected", KnowledgeBases: []string{f.KB.ID}}}, {ID: uuid.NewString(), Name: "replacement", TenantID: 1, Config: types.CustomAgentConfig{ModelID: "fixture-model", RerankModelID: "fixture-rerank", KBSelectionMode: "selected", KnowledgeBases: []string{f.KB.ID}}}}
	var shares []*types.AgentShare
	for _, agent := range agents {
		require.NoError(t, repository.NewCustomAgentRepository(f.DB).CreateAgent(f.Ctx, agent))
		share, err := f.AgentShares.ShareAgent(f.Ctx, agent.ID, org.ID, "owner", 1, types.OrgRoleViewer)
		require.NoError(t, err)
		shares = append(shares, share)
	}
	hits, err := f.KBs.HybridSearch(f.Ctx, f.KB.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	foreign := types.WithCaller(context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2)), types.Caller{TenantID: 2, UserID: "viewer", Role: types.TenantRoleViewer})
	_, err = f.Knowledge.GetSourceFile(foreign, hits[0].KnowledgeID)
	require.Error(t, err)
	enabled := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &enabled}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if len(c.Errors) > 0 && !c.Writer.Written() {
			c.JSON(http.StatusForbidden, gin.H{"error": c.Errors.Last().Error()})
		}
	})
	router.GET("/source/:id", middleware.RequireKBAccess(middleware.KBIDFromKnowledgeIDParam("id", f.Knowledge), types.OrgRoleViewer, f.KBs.(interfaces.KnowledgeBaseService), f.Shares, f.AgentShares, cfg), func(c *gin.Context) {
		view, readErr := f.Knowledge.GetSourceFile(c.Request.Context(), c.Param("id"))
		if readErr != nil {
			_ = c.Error(readErr)
			return
		}
		c.JSON(http.StatusOK, view)
	})
	router.GET("/chunk/:id", middleware.RequireKBAccess(middleware.KBIDFromChunkIDParam("id", f.Chunks), types.OrgRoleViewer, f.KBs.(interfaces.KnowledgeBaseService), f.Shares, f.AgentShares, cfg), func(c *gin.Context) {
		chunk, readErr := f.Chunks.GetChunkByIDOnly(c.Request.Context(), c.Param("id"))
		if readErr != nil {
			_ = c.Error(readErr)
			return
		}
		c.JSON(http.StatusOK, chunk)
	})
	for _, path := range []string{"/source/" + hits[0].KnowledgeID, "/chunk/" + hits[0].ID} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path+"?agent_id="+agents[0].ID+"&agent_source_tenant_id=1", nil).WithContext(foreign)
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		require.Contains(t, recorder.Body.String(), "getPushSchedule")
	}
	grant, err := access.ResolveKB(foreign, access.KBRequest{Caller: types.CallerFromContext(foreign), AgentID: agents[0].ID, AgentSourceTenantID: "1"}, f.KB, types.OrgRoleViewer, f.Shares, f.AgentShares)
	require.NoError(t, err)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.KB.ID, TenantID: 1, SourceIDs: []string{f.Source.ID}}}
	ctx, release, err := f.KBs.(interfaces.SourceReadService).BeginSourceRead(grant.Context(foreign), targets)
	require.NoError(t, err)
	defer release()
	_, err = f.Knowledge.GetSourceFile(ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	require.NoError(t, f.AgentShares.RemoveShare(f.Ctx, shares[0].ID, "owner", 1))
	_, err = f.Knowledge.GetSourceFile(ctx, hits[0].KnowledgeID)
	require.Error(t, err, "replacement reachable agent must not replace a revoked explicit selector")
	// Scope inheritance may change execution context, but not the original selector.
	ctx = access.WithSharedAgent(ctx, agents[1])
	_, err = f.Knowledge.GetSourceFile(ctx, hits[0].KnowledgeID)
	require.Error(t, err)
}
