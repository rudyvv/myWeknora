package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type sourceFileHandlerKnowledgeService struct {
	interfaces.KnowledgeService
	knowledge *types.Knowledge
	cursor    string
	versionID string
}

func (s *sourceFileHandlerKnowledgeService) GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error) {
	return s.knowledge, nil
}

func (s *sourceFileHandlerKnowledgeService) GetSourceFile(ctx context.Context, _ string, versionIDs ...string) (*types.SourceFileView, error) {
	s.cursor = source.RelationCursorFromContext(ctx)
	if len(versionIDs) > 0 {
		s.versionID = versionIDs[0]
	}
	return &types.SourceFileView{KnowledgeID: s.knowledge.ID, FileVersionID: s.versionID, RelationsNextCursor: "next"}, nil
}

func TestGetSourceFilePassesRelationCursorAndVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	knowledge := &types.Knowledge{ID: "knowledge-1", TenantID: 7, KnowledgeBaseID: "kb-1"}
	service := &sourceFileHandlerKnowledgeService{knowledge: knowledge}
	grant := &access.KBAccess{
		KnowledgeBase:     &types.KnowledgeBase{ID: knowledge.KnowledgeBaseID, TenantID: knowledge.TenantID},
		Caller:            types.Caller{TenantID: knowledge.TenantID, UserID: "user-1", Role: types.TenantRoleOwner},
		EffectiveTenantID: knowledge.TenantID,
		Permission:        types.OrgRoleAdmin,
	}
	handler := &KnowledgeHandler{kgService: service}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(middleware.KBAccessContextKey, grant)
		c.Next()
	})
	router.GET("/knowledge/:id/source", handler.GetSourceFile)

	const cursor, versionID = "opaque-next-page", "version-3"
	request := httptest.NewRequest(http.MethodGet, "/knowledge/knowledge-1/source?version_id="+versionID+"&relation_cursor="+cursor, nil)
	request = request.WithContext(types.WithCaller(request.Context(), grant.Caller))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, cursor, service.cursor)
	require.Equal(t, versionID, service.versionID)
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
	var body struct {
		Success bool                 `json:"success"`
		Data    types.SourceFileView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.True(t, body.Success)
	require.Equal(t, "next", body.Data.RelationsNextCursor)
}
