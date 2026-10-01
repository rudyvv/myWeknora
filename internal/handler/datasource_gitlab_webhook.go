package handler

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/handler/gitlabwebhook"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func (h *DataSourceHandler) gitLabWebhookService() (interfaces.GitLabWebhookService, bool) {
	service, ok := h.service.(interfaces.GitLabWebhookService)
	return service, ok
}

func (h *DataSourceHandler) GetGitLabWebhook(c *gin.Context) {
	service, ok := h.gitLabWebhookService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return
	}
	gitlabwebhook.NewAdmin(h.service, h.kbService, service).GetConfig(c)
}

func (h *DataSourceHandler) UpdateGitLabWebhook(c *gin.Context) {
	service, ok := h.gitLabWebhookService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return
	}
	gitlabwebhook.NewAdmin(h.service, h.kbService, service).UpdateConfig(c)
}

func (h *DataSourceHandler) TestGitLabWebhook(c *gin.Context) {
	service, ok := h.gitLabWebhookService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return
	}
	gitlabwebhook.NewAdmin(h.service, h.kbService, service).TestConnection(c)
}

func (h *DataSourceHandler) ReceiveGitLabPush(c *gin.Context) {
	service, ok := h.gitLabWebhookService()
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "GitLab webhook service unavailable"})
		return
	}
	gitlabwebhook.New(service).ReceivePush(c)
}
