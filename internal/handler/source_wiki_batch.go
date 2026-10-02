package handler

import (
	stderrors "errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// PreflightSourceWikiBatch validates a prospective batch and returns only a
// bounded plan preview. The query source_id also lets WikiReadScope pin this
// request to the exact source before the body is processed.
func (h *WikiPageHandler) PreflightSourceWikiBatch(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sourceID := c.Query("source_id")
	if sourceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id is required"})
		return
	}
	service, ok := h.sourceWiki.(interfaces.SourceWikiBatchPreflightService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki batch preflight service unavailable"})
		return
	}
	var req types.SourceWikiBatchPreflightRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	result, err := service.PreflightSourceWikiBatch(c.Request.Context(), kbID, sourceID, req)
	if err != nil {
		var appErr *apperrors.AppError
		if stderrors.As(err, &appErr) && appErr.HTTPCode != 0 {
			c.JSON(appErr.HTTPCode, gin.H{"error": appErr.Message})
			return
		}
		switch {
		case stderrors.Is(err, repository.ErrSourceWikiBatchNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "source Wiki batch or source is unavailable in this scope"})
		case stderrors.Is(err, repository.ErrSourceWikiBatchAlreadyActive), stderrors.Is(err, repository.ErrSourceWikiBatchInvalidState):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "source Wiki batch preflight failed"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func (h *WikiPageHandler) StartSourceWikiBatch(c *gin.Context) {
	kbID, _, err := h.validateWikiKB(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sourceID := c.Query("source_id")
	if sourceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source_id is required"})
		return
	}
	service, ok := h.sourceWiki.(interfaces.SourceWikiBatchStartService)
	if !ok {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "source Wiki batch start service unavailable"})
		return
	}
	var req types.SourceWikiBatchPreflightRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	batch, err := service.StartSourceWikiBatch(c.Request.Context(), kbID, sourceID, req)
	if err != nil {
		var appErr *apperrors.AppError
		if stderrors.As(err, &appErr) && appErr.HTTPCode != 0 {
			c.JSON(appErr.HTTPCode, gin.H{"error": appErr.Message})
			return
		}
		switch {
		case stderrors.Is(err, repository.ErrSourceWikiBatchNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "source Wiki batch or source is unavailable in this scope"})
		case stderrors.Is(err, repository.ErrSourceWikiBatchAlreadyActive), stderrors.Is(err, repository.ErrSourceWikiBatchInvalidState),
			stderrors.Is(err, repository.ErrSourceWikiBatchDeadline):
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "source Wiki batch could not be started"})
		}
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"data": batch})
}
