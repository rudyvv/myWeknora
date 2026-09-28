package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// PreviewSource requires the same Admin+ and tenant/API-key KB scope as the
// credential and sync endpoints. Draft input contains settings only.
func (h *DataSourceHandler) PreviewSource(c *gin.Context) {
	tenantID := h.getTenantID(c)
	if tenantID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	ctx := c.Request.Context()
	id := c.Param("id")
	if _, status, message := h.getOwnedDataSource(ctx, tenantID, id); status != http.StatusOK {
		c.JSON(status, gin.H{"error": message})
		return
	}
	var request struct {
		Settings map[string]interface{} `json:"settings"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid preview settings"})
		return
	}
	preview, err := h.service.PreviewSource(ctx, id, request.Settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": preview})
}
