package gitlabwebhook

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type AdminHandler struct {
	sources interfaces.DataSourceService
	kbs     interfaces.KnowledgeBaseService
	service interfaces.GitLabWebhookService
}

func NewAdmin(sources interfaces.DataSourceService, kbs interfaces.KnowledgeBaseService, service interfaces.GitLabWebhookService) *AdminHandler {
	return &AdminHandler{sources: sources, kbs: kbs, service: service}
}

type updateRequest struct {
	Enabled     *bool   `json:"enabled"`
	Secret      *string `json:"secret"`
	ClearSecret bool    `json:"clear_secret"`
}

func (h *AdminHandler) ownedSource(c *gin.Context) (uint64, bool) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return 0, false
	}
	if h.sources == nil || h.kbs == nil || h.service == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return 0, false
	}
	ds, err := h.sources.GetDataSource(c.Request.Context(), c.Param("id"))
	if err != nil || ds == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "data source not found"})
		return 0, false
	}
	kb, err := h.kbs.GetKnowledgeBaseByID(c.Request.Context(), ds.KnowledgeBaseID)
	if err != nil || kb == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return 0, false
	}
	if ds.TenantID != tenantID || kb.TenantID != tenantID {
		c.JSON(http.StatusForbidden, gin.H{"error": "access denied"})
		return 0, false
	}
	if err := types.AuthorizeTenantAPIKeyKnowledgeBases(c.Request.Context(), ds.KnowledgeBaseID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return 0, false
	}
	return tenantID, true
}

func (h *AdminHandler) GetConfig(c *gin.Context) {
	tenantID, ok := h.ownedSource(c)
	if !ok {
		return
	}
	status, err := h.service.GetGitLabWebhookStatus(c.Request.Context(), c.Param("id"), tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *AdminHandler) UpdateConfig(c *gin.Context) {
	tenantID, ok := h.ownedSource(c)
	if !ok {
		return
	}
	var request updateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	status, err := h.service.ConfigureGitLabWebhook(c.Request.Context(), c.Param("id"), tenantID, types.GitLabWebhookUpdate{
		Enabled: request.Enabled, Secret: request.Secret, ClearSecret: request.ClearSecret,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (h *AdminHandler) TestConnection(c *gin.Context) {
	tenantID, ok := h.ownedSource(c)
	if !ok {
		return
	}
	result, err := h.service.TestGitLabWebhook(c.Request.Context(), c.Param("id"), tenantID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
