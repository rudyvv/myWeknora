package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// WikiReadScope pins the full query alternatives for HTTP directory, body,
// summary and history projections. Services reuse the same lease.
func (h *WikiPageHandler) WikiReadScope(c *gin.Context) {
	reader, ok := h.wikiService.(interfaces.WikiReadService)
	if !ok {
		c.Next()
		return
	}
	split := func(key string) []string {
		values := []string{}
		for _, value := range c.QueryArray(key) {
			for _, part := range strings.Split(value, ",") {
				if part = strings.TrimSpace(part); part != "" {
					values = append(values, part)
				}
			}
		}
		return values
	}
	sourceIDs := split("source_ids")
	sourceIDs = append(sourceIDs, split("source_id")...)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: c.Param("kb_id"), SourceIDs: sourceIDs, KnowledgeIDs: split("knowledge_ids"), TagIDs: split("tag_ids")}}
	ctx, release, err := reader.BeginWikiRead(c.Request.Context(), targets)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	defer release()
	c.Request = c.Request.WithContext(ctx)
	c.Next()
}
func (h *WikiPageHandler) GenerateSourceModule(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.sourceWiki == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki service unavailable"})
		return
	}
	var req types.SourceWikiGenerateRequest
	if err = c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.KnowledgeBaseID = kbID
	attempt, err := h.sourceWiki.GenerateModule(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": attempt})
}
func (h *WikiPageHandler) ListSourceWikiAttempts(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.sourceWiki == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki service unavailable"})
		return
	}
	attempts, err := h.sourceWiki.ListAttempts(c.Request.Context(), kbID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": attempts})
}
func (h *WikiPageHandler) ReadSourceWikiEvidence(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.sourceWiki == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki service unavailable"})
		return
	}
	version, err := strconv.Atoi(c.DefaultQuery("version", "0"))
	if err != nil || version < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body version"})
		return
	}
	file, err := h.sourceWiki.ReadEvidence(c.Request.Context(), kbID, c.Query("slug"), version, c.Query("evidence_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "registered Wiki source is unavailable in this scope"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": file})
}

func (h *WikiPageHandler) ListSourceWikiBatches(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	service, ok := h.sourceWiki.(interfaces.SourceWikiBatchReadService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki batch service unavailable"})
		return
	}
	batches, err := service.ListSourceWikiBatches(c.Request.Context(), kbID, c.Query("source_id"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": batches})
}

func (h *WikiPageHandler) GetSourceWikiBatch(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	service, ok := h.sourceWiki.(interfaces.SourceWikiBatchReadService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki batch service unavailable"})
		return
	}
	batch, err := service.GetSourceWikiBatch(c.Request.Context(), kbID, c.Query("source_id"), c.Param("batch_id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "source Wiki batch is unavailable in this scope"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": batch})
}

func (h *WikiPageHandler) ListSourceWikiCoverage(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	service, ok := h.sourceWiki.(interfaces.SourceWikiBatchReadService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki batch service unavailable"})
		return
	}
	topics, err := service.ListSourceWikiCoverage(c.Request.Context(), kbID, c.Query("source_id"))
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": topics})
}
